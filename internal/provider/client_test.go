package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jellyfrog/terraform-provider-matterbridge/internal/fakematterbridge"
)

func startFake(t *testing.T, opts fakematterbridge.Options) *fakematterbridge.Server {
	t.Helper()
	server, err := fakematterbridge.Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	return server
}

func testClient(t *testing.T, server *fakematterbridge.Server, passwords ...string) *Client {
	t.Helper()
	c, err := NewClient(ClientConfig{URL: server.URL, Passwords: passwords, Timeout: time.Second, RestartTimeout: 10 * time.Second, PollInterval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestClientReads(t *testing.T) {
	server := startFake(t, fakematterbridge.Options{})
	c := testClient(t, server, "not-set-yet") // no password set: the client falls back to ""
	ctx := context.Background()
	info, err := c.Settings(ctx)
	if err != nil || info["bridgeMode"] != "bridge" || info["matterPort"] != 5540.0 {
		t.Fatalf("settings: %v %v", info, err)
	}
	if server.Password() != "" {
		t.Fatal("password changed")
	}
	plugins, err := c.Plugins(ctx)
	if err != nil || len(plugins) != 2 || plugins[0].Name != fakematterbridge.Zigbee2MQTT || plugins[0].Version != "3.3.0" {
		t.Fatalf("plugins: %+v %v", plugins, err)
	}
	if plugins[0].SchemaJSON["properties"] == nil || plugins[0].ConfigJSON["topic"] != "zigbee2mqtt" {
		t.Fatalf("plugin json: %+v", plugins[0])
	}
	devices, err := c.Devices(ctx, fakematterbridge.Hass)
	if err != nil || len(devices) != 1 || devices[0].DeviceName != "Café Fridge Plug" {
		t.Fatalf("devices: %+v %v", devices, err)
	}
	selectable, err := c.SelectDevices(ctx, fakematterbridge.Zigbee2MQTT)
	if err != nil || len(selectable) != 5 || len(selectable[1].Entities) == 0 {
		t.Fatalf("select devices: %+v %v", selectable, err)
	}
	m, err := c.Matter(ctx, "Matterbridge")
	if err != nil || !m.Commissioned || len(m.Fabrics) != 1 || m.Fabrics[0].RootVendorID != 4937 || m.QRPairingCode == "" {
		t.Fatalf("matter: %+v %v", m, err)
	}
	if calls := server.Calls(); len(calls) != 0 {
		t.Fatalf("reads changed something: %v", calls)
	}
}

func TestClientPasswords(t *testing.T) {
	server := startFake(t, fakematterbridge.Options{Password: "old-secret"})
	ctx := context.Background()

	_, err := testClient(t, server, "wrong-secret").Settings(ctx)
	if !errors.Is(err, errRejected) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("wrong password: %v", err)
	}

	// password is the new one, old_password still works.
	c := testClient(t, server, "new-secret", "old-secret")
	if _, err := c.Settings(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPassword(ctx, "new-secret"); err != nil {
		t.Fatal(err)
	}
	if server.Password() != "new-secret" {
		t.Fatalf("password: %q", server.Password())
	}
	// The open connection stays valid. A new connection uses the new password.
	if _, err := c.Settings(ctx); err != nil {
		t.Fatal(err)
	}
	c.dropConnection()
	time.Sleep(20 * time.Millisecond)
	if _, err := c.Settings(ctx); err != nil {
		t.Fatalf("after reconnect: %v", err)
	}
	if c.passwords[0] != "new-secret" {
		t.Fatal("the client does not use the new password first")
	}
}

func TestClientNoAnswer(t *testing.T) {
	server := startFake(t, fakematterbridge.Options{})
	c := testClient(t, server)
	err := c.SetConfig(context.Background(), "setbridgemode", "hub")
	if err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("got %v", err)
	}
	if err := c.SetConfig(context.Background(), "setmatterport", "6000"); err == nil || !strings.Contains(err.Error(), "reset matter commissioning port") {
		t.Fatalf("got %v", err)
	}
}

func TestClientInstallAndRestart(t *testing.T) {
	server := startFake(t, fakematterbridge.Options{})
	c := testClient(t, server)
	ctx := context.Background()
	if err := c.Install(ctx, fakematterbridge.Hass+"@9.9.9"); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("install of a version that does not exist: %v", err)
	}
	if err := c.Install(ctx, fakematterbridge.Hass+"@1.5.0"); err != nil {
		t.Fatal(err)
	}
	if p, _ := c.Plugin(ctx, fakematterbridge.Hass); p.Version != "1.6.0" {
		t.Fatalf("version before restart: %s", p.Version)
	}
	if err := c.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	if p, err := c.Plugin(ctx, fakematterbridge.Hass); err != nil || p.Version != "1.5.0" {
		t.Fatalf("version after restart: %+v %v", p, err)
	}
	if server.Calls()["/api/restart"] != 1 {
		t.Fatalf("calls: %v", server.Calls())
	}
}

func TestClientRestartTimeoutTellsPhase(t *testing.T) {
	server := startFake(t, fakematterbridge.Options{RestartDelay: 5 * time.Second})
	c, err := NewClient(ClientConfig{URL: server.URL, Timeout: time.Second, RestartTimeout: 500 * time.Millisecond, PollInterval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	err = c.Restart(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Matterbridge did not answer") {
		t.Fatalf("got %v", err)
	}
}

// A read survives a dropped connection: the client connects again and sends the read again.
func TestClientReconnects(t *testing.T) {
	server := startFake(t, fakematterbridge.Options{})
	c := testClient(t, server)
	ctx := context.Background()
	for i := range 5 {
		server.DropConnections()
		if _, err := c.Plugins(ctx); err != nil {
			t.Fatalf("read %d after a dropped connection: %v", i, err)
		}
	}
	// A change is not sent again, but it must not hang: it succeeds on a new connection, or fails as closed.
	server.DropConnections()
	if err := c.SetConfig(ctx, "setmbloglevel", "Info"); err != nil && !errors.Is(err, errClosed) {
		t.Fatalf("change after a dropped connection: %v", err)
	}
}

// npm waits for the snackbar of its own package, and times out without one.
func TestClientNpm(t *testing.T) {
	server := startFake(t, fakematterbridge.Options{InstallDelay: 200 * time.Millisecond})
	c := testClient(t, server)
	ctx := context.Background()
	// Two installs at the same time: each gets the result for its own package.
	errs := make(chan error, 2)
	go func() { errs <- c.Install(ctx, fakematterbridge.Hass+"@1.5.0") }()
	go func() { errs <- c.Install(ctx, fakematterbridge.Shelly+"@9.9.9") }()
	var failures []string
	for range 2 {
		if err := <-errs; err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) != 1 || !strings.Contains(failures[0], fakematterbridge.Shelly+"@9.9.9 failed") {
		t.Fatalf("failures: %v", failures)
	}
	if server.Installed(fakematterbridge.Hass) != "1.5.0" {
		t.Fatalf("hass on disk: %s", server.Installed(fakematterbridge.Hass))
	}
	if err := c.Uninstall(ctx, fakematterbridge.Hass); err != nil || server.Installed(fakematterbridge.Hass) != "" {
		t.Fatalf("uninstall: %v", err)
	}

	slow := startFake(t, fakematterbridge.Options{InstallDelay: 2 * time.Second})
	sc, err := NewClient(ClientConfig{URL: slow.URL, Timeout: time.Second, RestartTimeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sc.Close)
	if err := sc.Install(ctx, fakematterbridge.Hass); err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("slow npm: %v", err)
	}
}
