package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// The client uses only the WebSocket API of the Matterbridge frontend. The WebSocket upgrade checks
// ?password=, so it is the authentication. (The REST API uses /api/login, which allows the IP address only while
// a WebSocket client is connected.) Each message is {id, src, dst: "Matterbridge", method, params}. The answer
// has the same id. Broadcasts have id 0.

type ClientConfig struct {
	// URL is the frontend, for example http://192.168.1.10:8283.
	URL string
	// Passwords are tried in sequence. The first that Matterbridge accepts is used.
	Passwords []string
	// Timeout is for each answer. The default is 30 seconds.
	Timeout time.Duration
	// RestartTimeout is for npm and for a restart of Matterbridge. The default is 5 minutes.
	RestartTimeout time.Duration
	// PollInterval is how frequently the client tries again after a restart. The default is 2 seconds.
	PollInterval time.Duration
}

type Client struct {
	cfg   ClientConfig
	wsURL string

	// gate stops all requests while Matterbridge restarts. ops makes the changes of resources one at a time,
	// because a resource can restart Matterbridge.
	gate sync.RWMutex
	ops  sync.Mutex

	mu        sync.Mutex
	conn      *websocket.Conn
	writeMu   *sync.Mutex
	passwords []string
	nextID    int64
	pending   map[int64]chan wsMessage
	subs      map[chan wsMessage]struct{}
}

type wsMessage struct {
	ID       int64           `json:"id"`
	Method   string          `json:"method"`
	Success  bool            `json:"success"`
	Error    any             `json:"error"`
	Response json.RawMessage `json:"response"`
}

var errClosed = errors.New("the connection to Matterbridge closed")

// errRejected is the error when Matterbridge does not accept any password. It does not contain the passwords.
var errRejected = errors.New("Matterbridge rejected the password. Set password (or MATTERBRIDGE_PASSWORD) to the frontend password. While you change the password, set old_password to the old password")

func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.RestartTimeout == 0 {
		cfg.RestartTimeout = 5 * time.Minute
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 2 * time.Second
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("use a URL like http://192.168.1.10:8283")
	}
	switch u.Scheme {
	case "http", "ws":
		u.Scheme = "ws"
	case "https", "wss":
		u.Scheme = "wss"
	default:
		return nil, fmt.Errorf("the scheme must be http, https, ws or wss")
	}
	u.Path, u.RawQuery, u.Fragment = "/", "", ""
	passwords := []string{}
	for _, p := range cfg.Passwords {
		if !slices.Contains(passwords, p) {
			passwords = append(passwords, p)
		}
	}
	// If Matterbridge has no password, the WebSocket accepts only "". Try it last, so that the configured password
	// can be the password that the provider sets.
	if !slices.Contains(passwords, "") {
		passwords = append(passwords, "")
	}
	return &Client{cfg: cfg, wsURL: u.String(), passwords: passwords, pending: map[int64]chan wsMessage{}, subs: map[chan wsMessage]struct{}{}}, nil
}

// connect opens the WebSocket if it is not open. The caller holds c.mu.
func (c *Client) connect(ctx context.Context) error {
	if c.conn != nil {
		return nil
	}
	dialer := websocket.Dialer{HandshakeTimeout: c.cfg.Timeout, Proxy: http.ProxyFromEnvironment}
	for _, password := range slices.Clone(c.passwords) {
		conn, resp, err := dialer.DialContext(ctx, c.wsURL+"?password="+url.QueryEscape(password), nil)
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusUnauthorized {
				continue
			}
			return fmt.Errorf("cannot connect to Matterbridge at %s: %w", c.cfg.URL, sanitize(err, password))
		}
		conn.SetReadLimit(64 << 20)
		c.preferPassword(password)
		c.conn, c.writeMu = conn, &sync.Mutex{}
		go c.read(conn)
		return nil
	}
	return errRejected
}

// sanitize removes the password from an error, in case it contains the URL.
func sanitize(err error, password string) error {
	if password == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), url.QueryEscape(password), "[redacted]")
	return errors.New(strings.ReplaceAll(msg, password, "[redacted]"))
}

func (c *Client) read(conn *websocket.Conn) {
	for {
		var msg wsMessage
		if err := conn.ReadJSON(&msg); err != nil {
			var syntax *json.SyntaxError
			if errors.As(err, &syntax) {
				continue
			}
			break
		}
		c.mu.Lock()
		ch := c.pending[msg.ID]
		if msg.ID != 0 && ch != nil {
			delete(c.pending, msg.ID)
			ch <- msg
		} else {
			for sub := range c.subs {
				select {
				case sub <- msg:
				default:
				}
			}
		}
		c.mu.Unlock()
	}
	_ = conn.Close()
	c.mu.Lock()
	if c.conn == conn {
		c.conn = nil
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
	}
	c.mu.Unlock()
}

// preferPassword puts the password that works first, also for later connections. The caller holds c.mu.
func (c *Client) preferPassword(password string) {
	c.passwords = append([]string{password}, slices.DeleteFunc(slices.Clone(c.passwords), func(p string) bool { return p == password })...)
}

// Call sends a request that changes something, and waits for the answer. out gets the response.
func (c *Client) Call(ctx context.Context, method string, params map[string]any, out any) error {
	c.gate.RLock()
	defer c.gate.RUnlock()
	return c.callOnce(ctx, method, params, out)
}

// Query sends a request that only reads. If the connection closes, it sends the request again.
func (c *Client) Query(ctx context.Context, method string, params map[string]any, out any) error {
	c.gate.RLock()
	defer c.gate.RUnlock()
	return c.query(ctx, method, params, out)
}

func (c *Client) query(ctx context.Context, method string, params map[string]any, out any) error {
	err := c.callOnce(ctx, method, params, out)
	if errors.Is(err, errClosed) {
		err = c.callOnce(ctx, method, params, out)
	}
	return err
}

func (c *Client) callOnce(ctx context.Context, method string, params map[string]any, out any) error {
	id, ch, err := c.send(ctx, method, params, true)
	if err != nil {
		return err
	}
	timer := time.NewTimer(c.cfg.Timeout)
	defer timer.Stop()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id) // if no answer came
		c.mu.Unlock()
	}()
	var msg wsMessage
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("Matterbridge did not answer %s in %s. Matterbridge does not answer some requests with an incorrect value", method, c.cfg.Timeout)
	case m, open := <-ch:
		if !open {
			return fmt.Errorf("%s: %w", method, errClosed)
		}
		msg = m
	}
	if msg.Error != nil && msg.Error != "" {
		return fmt.Errorf("%s: %v", method, msg.Error)
	}
	if out != nil && len(msg.Response) > 0 {
		if err := json.Unmarshal(msg.Response, out); err != nil {
			return fmt.Errorf("%s: cannot read the answer: %w", method, err)
		}
	}
	return nil
}

// send writes a request. If wait is true, it returns the channel that gets the answer.
func (c *Client) send(ctx context.Context, method string, params map[string]any, wait bool) (int64, chan wsMessage, error) {
	c.mu.Lock()
	if err := c.connect(ctx); err != nil {
		c.mu.Unlock()
		return 0, nil, err
	}
	c.nextID++
	id := c.nextID
	var ch chan wsMessage
	if wait {
		ch = make(chan wsMessage, 1)
		c.pending[id] = ch
	}
	conn, writeMu := c.conn, c.writeMu
	c.mu.Unlock()
	if params == nil {
		params = map[string]any{}
	}
	writeMu.Lock()
	err := conn.WriteJSON(map[string]any{"id": id, "src": "OpenTofu", "dst": "Matterbridge", "method": method, "params": params})
	writeMu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		_ = conn.Close()
		return id, nil, fmt.Errorf("%s: %w", method, errClosed)
	}
	return id, ch, nil
}

// subscribe returns a channel with the broadcasts. Call the returned function to stop.
func (c *Client) subscribe() (chan wsMessage, func()) {
	ch := make(chan wsMessage, 256)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		delete(c.subs, ch)
		c.mu.Unlock()
	}
}

// Lock makes the changes of one resource happen alone. Call the returned function to unlock.
func (c *Client) Lock() func() {
	c.ops.Lock()
	return c.ops.Unlock
}

func (c *Client) Close() { c.dropConnection() }

// Settings is matterbridgeInformation from /api/settings.
func (c *Client) Settings(ctx context.Context) (map[string]any, error) {
	return c.settings(ctx, c.Query)
}

func (c *Client) settings(ctx context.Context, call func(context.Context, string, map[string]any, any) error) (map[string]any, error) {
	var answer struct {
		Info map[string]any `json:"matterbridgeInformation"`
	}
	if err := call(ctx, "/api/settings", nil, &answer); err != nil {
		return nil, err
	}
	if answer.Info == nil {
		return nil, fmt.Errorf("/api/settings: the answer has no matterbridgeInformation")
	}
	return answer.Info, nil
}

type Plugin struct {
	Name              string         `json:"name"`
	Type              string         `json:"type"`
	Version           string         `json:"version"`
	LatestVersion     string         `json:"latestVersion"`
	Description       string         `json:"description"`
	Enabled           bool           `json:"enabled"`
	Loaded            bool           `json:"loaded"`
	Started           bool           `json:"started"`
	Configured        bool           `json:"configured"`
	Error             bool           `json:"error"`
	RestartRequired   bool           `json:"restartRequired"`
	RegisteredDevices int64          `json:"registeredDevices"`
	ConfigJSON        map[string]any `json:"configJson"`
	SchemaJSON        map[string]any `json:"schemaJson"`
}

func (c *Client) Plugins(ctx context.Context) ([]Plugin, error) {
	var plugins []Plugin
	err := c.Query(ctx, "/api/plugins", nil, &plugins)
	return plugins, err
}

// Plugin returns the plugin, or nil if Matterbridge does not have it.
func (c *Client) Plugin(ctx context.Context, name string) (*Plugin, error) {
	plugins, err := c.Plugins(ctx)
	if err != nil {
		return nil, err
	}
	for i := range plugins {
		if plugins[i].Name == name {
			return &plugins[i], nil
		}
	}
	return nil, nil
}

type Device struct {
	Plugin       string  `json:"plugin"`
	Type         string  `json:"type"`
	Number       *int64  `json:"number"`
	ID           *string `json:"id"`
	DeviceName   string  `json:"deviceName"`
	SerialNumber string  `json:"serialNumber"`
	UniqueID     string  `json:"uniqueId"`
	ProductURL   string  `json:"productUrl"`
	ConfigURL    string  `json:"configUrl"`
	Reachable    bool    `json:"reachable"`
	PowerSource  string  `json:"powerSource"`
}

// Devices returns the registered devices. If plugin is "", it returns the devices of all plugins.
func (c *Client) Devices(ctx context.Context, plugin string) ([]Device, error) {
	params := map[string]any{"extended": true}
	if plugin != "" {
		params["pluginName"] = plugin
	}
	var devices []Device
	err := c.Query(ctx, "/api/devices", params, &devices)
	return devices, err
}

type SelectDevice struct {
	Plugin       string   `json:"plugin"`
	SerialNumber string   `json:"serialNumber"`
	DeviceName   string   `json:"deviceName"`
	ConfigURL    string   `json:"configUrl"`
	Entities     []Entity `json:"entities"`
}

// Entity is a feature of a select device, for example device_temperature.
type Entity struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// SelectDevices returns the devices that the plugin offers for the white list and the black list.
func (c *Client) SelectDevices(ctx context.Context, plugin string) ([]SelectDevice, error) {
	var devices []SelectDevice
	err := c.Query(ctx, "/api/select/devices", map[string]any{"plugin": plugin}, &devices)
	return devices, err
}

type Fabric struct {
	FabricIndex    int64  `json:"fabricIndex"`
	FabricID       string `json:"fabricId"`
	NodeID         string `json:"nodeId"`
	RootNodeID     string `json:"rootNodeId"`
	RootVendorID   int64  `json:"rootVendorId"`
	RootVendorName string `json:"rootVendorName"`
	Label          string `json:"label"`
}

type Matter struct {
	ID                string            `json:"id"`
	Online            bool              `json:"online"`
	Commissioned      bool              `json:"commissioned"`
	Advertising       bool              `json:"advertising"`
	WindowStatus      int64             `json:"windowStatus"`
	QRPairingCode     string            `json:"qrPairingCode"`
	ManualPairingCode string            `json:"manualPairingCode"`
	SerialNumber      string            `json:"serialNumber"`
	Fabrics           []Fabric          `json:"fabricInformations"`
	Sessions          []json.RawMessage `json:"sessionInformations"`
}

// Matter returns the Matter server node: "Matterbridge" in bridge mode, or a plugin name in childbridge mode.
// The request only reads, because it has no command parameters.
func (c *Client) Matter(ctx context.Context, node string) (*Matter, error) {
	return c.MatterCommand(ctx, node, "")
}

// MatterCommand sends a command for the server node: "startCommission", "stopCommission" or "advertise".
func (c *Client) MatterCommand(ctx context.Context, node, command string) (*Matter, error) {
	params, call := map[string]any{"id": node}, c.Query
	if command != "" {
		params[command], call = true, c.Call
	}
	var m Matter
	if err := call(ctx, "/api/matter", params, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// SetConfig sends /api/config.
func (c *Client) SetConfig(ctx context.Context, name string, value any) error {
	return c.Call(ctx, "/api/config", map[string]any{"name": name, "value": value}, nil)
}

// SetPassword changes the frontend password. The connection stays open. Later connections use the new password.
func (c *Client) SetPassword(ctx context.Context, password string) error {
	if err := c.SetConfig(ctx, "setpassword", password); err != nil {
		return err
	}
	c.mu.Lock()
	c.preferPassword(password)
	c.mu.Unlock()
	return nil
}

// Install runs npm install for packageName (for example matterbridge-hass@1.6.0) and waits until npm is done.
// The frontend tells the result only with a snackbar broadcast.
func (c *Client) Install(ctx context.Context, packageName string) error {
	return c.npm(ctx, "install", packageName, "Installed package "+packageName, "Package "+packageName+" not installed")
}

func (c *Client) Uninstall(ctx context.Context, packageName string) error {
	return c.npm(ctx, "uninstall", packageName, "Uninstalled package "+packageName, "Package "+packageName+" not uninstalled")
}

func (c *Client) npm(ctx context.Context, command, packageName, success, failure string) error {
	c.gate.RLock()
	defer c.gate.RUnlock()
	events, stop := c.subscribe()
	defer stop()
	params := map[string]any{"packageName": packageName}
	if command == "install" {
		params["restart"] = false
	}
	if err := c.callOnce(ctx, "/api/"+command, params, nil); err != nil {
		return err
	}
	timer := time.NewTimer(c.cfg.RestartTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("npm %s %s did not finish in %s", command, packageName, c.cfg.RestartTimeout)
		case msg := <-events:
			if msg.Method != "snackbar" {
				continue
			}
			var snack struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(msg.Response, &snack)
			switch snack.Message {
			case success:
				return nil
			case failure:
				return fmt.Errorf("npm %s %s failed. The Matterbridge log shows the npm output", command, packageName)
			}
		}
	}
}

// Restart restarts Matterbridge and waits until it is back with its plugins. HomeKit and other controllers lose
// the devices during the restart.
func (c *Client) Restart(ctx context.Context) error {
	c.gate.Lock()
	defer c.gate.Unlock()
	before, err := c.settings(ctx, c.query)
	if err != nil {
		return err
	}
	startup := fmt.Sprint(before["startupAt"])
	// restartProcess stops the process before it answers, so do not wait for an answer.
	if _, _, err := c.send(ctx, "/api/restart", nil, false); err != nil {
		return err
	}
	deadline := time.Now().Add(c.cfg.RestartTimeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("Matterbridge did not start again in %s after the restart", c.cfg.RestartTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.cfg.PollInterval):
		}
		info, err := c.settings(ctx, c.callOnce)
		if errors.Is(err, errRejected) {
			return err
		}
		if err != nil {
			continue
		}
		if fmt.Sprint(info["startupAt"]) == startup {
			// The old process still answers. Close, so that the next try connects to the new process.
			c.dropConnection()
			continue
		}
		var plugins []Plugin
		if err := c.callOnce(ctx, "/api/plugins", nil, &plugins); err != nil {
			continue
		}
		if !slices.ContainsFunc(plugins, func(p Plugin) bool { return p.Enabled && !p.Error && !(p.Started && p.Configured) }) {
			return nil
		}
	}
}

func (c *Client) dropConnection() {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}
