// Package realmb starts a real Matterbridge in Docker (the image luligu/matterbridge) for the acceptance tests
// TestAccDocker* (TF_ACC=1 and TF_ACC_DOCKER=1). Each test gets its own container, because the tests change
// the settings and the password, and restart Matterbridge.
//
// The plugins come from npm, so the tests need the internet. If HTTPS_PROXY is a proxy on this computer
// (127.0.0.1 or localhost), the container uses the network of the host and the proxy. If the host has no IPv6
// (for example a sandbox with a kernel without IPv6), matter.js cannot start. Then noipv6.cjs makes its IPv6
// sockets IPv4 sockets.
package realmb

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	// EnvEnable enables the tests that need Docker.
	EnvEnable = "TF_ACC_DOCKER"
	// EnvImageTag selects the image tag. The default is DefaultImageTag.
	EnvImageTag = "MATTERBRIDGE_IMAGE_TAG"
	// DefaultImageTag is the Matterbridge version that the provider targets.
	DefaultImageTag = "3.11.0"

	image        = "luligu/matterbridge"
	frontendPort = "8283/tcp"
)

//go:embed noipv6.cjs
var noIPv6Shim []byte

// Instance is a running Matterbridge without a frontend password.
type Instance struct {
	// URL is the frontend, for example http://127.0.0.1:32768.
	URL string
	// ImageTag is the image tag that the container runs.
	ImageTag string

	ctr testcontainers.Container
}

// Enabled tells if the Docker tests run (TF_ACC and TF_ACC_DOCKER are set).
func Enabled() bool { return os.Getenv("TF_ACC") != "" && os.Getenv(EnvEnable) != "" }

// Start starts a Matterbridge for the test and removes it when the test ends. If the test fails, the test log gets
// the log of Matterbridge. Without TF_ACC and TF_ACC_DOCKER, it skips the test.
func Start(t *testing.T) *Instance {
	t.Helper()
	if !Enabled() {
		t.Skip("set TF_ACC=1 and " + EnvEnable + "=1 to run the tests with a real Matterbridge in Docker")
	}
	mb, err := start(context.Background(), t.TempDir())
	if mb != nil && mb.ctr != nil {
		t.Cleanup(func() {
			if t.Failed() || os.Getenv("REALMB_LOGS") != "" {
				t.Logf("--- Matterbridge log ---\n%s", mb.Logs())
			}
			if err := mb.ctr.Terminate(context.Background(), testcontainers.StopTimeout(5*time.Second)); err != nil {
				t.Logf("cannot remove the Matterbridge container: %v", err)
			}
		})
	}
	if err != nil {
		t.Fatalf("cannot start Matterbridge: %v", err)
	}
	return mb
}

// Logs returns the log of Matterbridge.
func (mb *Instance) Logs() string {
	r, err := mb.ctr.Logs(context.Background())
	if err != nil {
		return err.Error()
	}
	defer r.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

// localProxy returns HTTPS_PROXY if it is a proxy on this computer.
func localProxy() string {
	p := os.Getenv("HTTPS_PROXY")
	if p == "" {
		p = os.Getenv("https_proxy")
	}
	u, err := url.Parse(p)
	if err != nil || u.Host == "" {
		return ""
	}
	if h := u.Hostname(); h == "127.0.0.1" || h == "localhost" {
		return p
	}
	return ""
}

// hostHasIPv6 tells if the kernel of the Docker host supports IPv6. The tests run on the Docker host.
func hostHasIPv6() bool {
	_, err := os.Stat("/proc/sys/net/ipv6")
	return err == nil
}

func start(ctx context.Context, dir string) (*Instance, error) {
	tag := os.Getenv(EnvImageTag)
	if tag == "" {
		tag = DefaultImageTag
	}
	req := testcontainers.ContainerRequest{
		Image:        image + ":" + tag,
		ExposedPorts: []string{frontendPort},
		Env:          map[string]string{"TZ": "UTC"},
		WaitingFor:   wait.ForHTTP("/").WithPort(frontendPort).WithStartupTimeout(3 * time.Minute),
	}
	var mounts []mount.Mount
	if !hostHasIPv6() {
		shim := filepath.Join(dir, "noipv6.cjs")
		if err := os.WriteFile(shim, noIPv6Shim, 0o644); err != nil {
			return nil, err
		}
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: shim, Target: "/shim/noipv6.cjs", ReadOnly: true})
		req.Env["NODE_OPTIONS"] = "--require /shim/noipv6.cjs"
	}
	base := ""
	proxy := localProxy()
	if proxy != "" {
		port, err := freePort()
		if err != nil {
			return nil, err
		}
		// npm reads HTTPS_PROXY. The fetch of Node (the latest versions of the plugins) reads it only with
		// NODE_USE_ENV_PROXY.
		req.Env["HTTPS_PROXY"], req.Env["NO_PROXY"], req.Env["NODE_USE_ENV_PROXY"] = proxy, "localhost,127.0.0.1", "1"
		if ca := os.Getenv("SSL_CERT_FILE"); ca != "" {
			mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: ca, Target: "/proxy/ca.crt", ReadOnly: true})
			req.Env["NODE_EXTRA_CA_CERTS"] = "/proxy/ca.crt"
		}
		// With the network of the host, the frontend listens on a free port of this computer.
		req.Cmd = []string{"matterbridge", "--docker", "-frontend", strconv.Itoa(port)}
		req.ExposedPorts, req.WaitingFor = nil, nil
		base = fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	req.HostConfigModifier = func(hc *container.HostConfig) {
		if proxy != "" {
			hc.NetworkMode = "host"
		}
		hc.Mounts = append(hc.Mounts, mounts...)
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	mb := &Instance{ImageTag: tag, ctr: c}
	if err != nil {
		return mb, err
	}
	if base == "" {
		if base, err = c.PortEndpoint(ctx, frontendPort, "http"); err != nil {
			return mb, err
		}
	} else if err := waitForURL(ctx, base+"/"); err != nil {
		return mb, err
	}
	mb.URL = base
	return mb, nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitForURL waits until the URL answers 200.
func waitForURL(ctx context.Context, u string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s did not answer: %w", u, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}
