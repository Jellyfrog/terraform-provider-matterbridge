// Package fakematterbridge is a fake Matterbridge frontend (version 3.11.0) for tests. It serves the WebSocket API
// on 127.0.0.1 as packages/core/src/frontend.ts does, with the same quirks:
//
//   - The WebSocket upgrade is the authentication: ?password= must be the frontend password.
//   - Some /api/config names do not answer if the value is incorrect.
//   - /api/savepluginconfig answers before the config changes (fire and forget).
//   - /api/install answers at once. A snackbar broadcast tells when npm is done.
//   - /api/plugins shows a new plugin version only after /api/restart.
//   - /api/restart drops all connections without an answer. The server is down for RestartDelay.
//
// The plugins (matterbridge-zigbee2mqtt 3.3.0 and matterbridge-hass 1.6.0) have the configJson and schemaJson of
// these versions. The devices and select devices have the format of Matterbridge 3.11.0, with made-up names and
// addresses.
package fakematterbridge

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	//go:embed testdata/plugins.json
	pluginsJSON []byte
	//go:embed testdata/devices.json
	devicesJSON []byte
	//go:embed testdata/select_devices.json
	selectDevicesJSON []byte
)

// The plugin names in the fake installation.
const (
	Zigbee2MQTT = "matterbridge-zigbee2mqtt"
	Hass        = "matterbridge-hass"
	// Shelly is in the npm registry of the fake, but it is not installed.
	Shelly = "matterbridge-shelly"
)

// Registry is the versions that the fake npm can install, oldest first.
var Registry = map[string][]string{
	Zigbee2MQTT: {"3.2.0", "3.3.0", "3.3.1"},
	Hass:        {"1.5.0", "1.6.0"},
	Shelly:      {"2.0.0", "2.1.0"},
}

type Options struct {
	// Password is the frontend password. As in Matterbridge, the WebSocket password must be exactly this, also if it is empty.
	Password string
	// RestartDelay is how long the server is down after /api/restart. The default is 300 ms.
	RestartDelay time.Duration
	// SaveDelay is how long after /api/savepluginconfig the config changes. The default is 50 ms.
	SaveDelay time.Duration
	// InstallDelay is how long npm takes. The default is 100 ms.
	InstallDelay time.Duration
}

// Plugin is a plugin as /api/plugins shows it.
type Plugin struct {
	Type              string         `json:"type"`
	Name              string         `json:"name"`
	Version           string         `json:"version"`
	Description       string         `json:"description"`
	LatestVersion     string         `json:"latestVersion"`
	Enabled           bool           `json:"enabled"`
	Loaded            bool           `json:"loaded"`
	Started           bool           `json:"started"`
	Configured        bool           `json:"configured"`
	Error             bool           `json:"error"`
	RestartRequired   bool           `json:"restartRequired"`
	RegisteredDevices int            `json:"registeredDevices"`
	ConfigJSON        map[string]any `json:"configJson"`
	SchemaJSON        map[string]any `json:"schemaJson"`
}

// Calls counts the requests that change something, by method (and by /api/config name, for example
// "/api/config setbridgemode").
type Calls map[string]int

type Server struct {
	URL string

	opts     Options
	listener net.Listener
	http     *http.Server

	connMu sync.Mutex
	conns  map[*websocket.Conn]*sync.Mutex

	mu            sync.Mutex
	password      string
	down          bool
	plugins       []*Plugin
	devices       []map[string]any
	selectDevices []map[string]any
	installed     map[string]string // npm package → version on disk
	registered    map[string]int    // plugin → the devices that it registers when Matterbridge starts
	info          map[string]any    // matterbridgeInformation
	stored        map[string]any    // node context values that apply after a restart
	matter        map[string]any
	calls         Calls
	startupAt     int64
}

func Start(opts Options) (*Server, error) {
	if opts.RestartDelay == 0 {
		opts.RestartDelay = 300 * time.Millisecond
	}
	if opts.SaveDelay == 0 {
		opts.SaveDelay = 50 * time.Millisecond
	}
	if opts.InstallDelay == 0 {
		opts.InstallDelay = 100 * time.Millisecond
	}
	s := &Server{opts: opts, password: opts.Password, conns: map[*websocket.Conn]*sync.Mutex{}, calls: Calls{}, stored: map[string]any{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s.listener = l
	s.URL = "http://" + l.Addr().String()
	s.http = &http.Server{Handler: http.HandlerFunc(s.serve)}
	go func() { _ = s.http.Serve(l) }()
	return s, nil
}

func (s *Server) load() error {
	if err := json.Unmarshal(pluginsJSON, &s.plugins); err != nil {
		return err
	}
	if err := json.Unmarshal(devicesJSON, &s.devices); err != nil {
		return err
	}
	if err := json.Unmarshal(selectDevicesJSON, &s.selectDevices); err != nil {
		return err
	}
	s.installed, s.registered = map[string]string{}, map[string]int{}
	for _, p := range s.plugins {
		p.Loaded, p.Started, p.Configured = p.Enabled, p.Enabled, p.Enabled
		s.installed[p.Name] = p.Version
		s.registered[p.Name] = p.RegisteredDevices
	}
	s.startupAt = time.Now().UnixMilli()
	s.info = map[string]any{
		"matterbridgeVersion": "3.11.0", "matterbridgeLatestVersion": "3.11.0", "bridgeMode": "bridge", "restartMode": "docker",
		"virtualMode": "outlet", "bridgeStatus": "started", "loggerLevel": "info", "fileLogger": false, "matterLoggerLevel": "info",
		"matterFileLogger": false, "matterPort": 5540, "matterDiscriminator": 527, "matterPasscode": 9327779, "readOnly": false,
		"restartRequired": false, "fixedRestartRequired": false, "updateRequired": false,
	}
	s.matter = map[string]any{
		"id": "Matterbridge", "online": true, "commissioned": true, "advertising": false, "advertiseTime": 0, "windowStatus": 0,
		"qrPairingCode": "MT:Y.K90-Q000KA0648G00", "manualPairingCode": "34970112332", "serialNumber": "SN0000000000000001",
		"fabricInformations": []any{
			map[string]any{"fabricIndex": 1, "fabricId": "1000001", "nodeId": "2000002", "rootNodeId": "3000003", "rootVendorId": 4937, "rootVendorName": "(AppleHome)", "label": "My Home"},
		},
		"sessionInformations": []any{map[string]any{"name": "secure/1", "isPeerActive": true}},
	}
	return nil
}

func (s *Server) Close() {
	s.connMu.Lock()
	for c := range s.conns {
		_ = c.Close()
	}
	s.connMu.Unlock()
	_ = s.http.Close()
}

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	down, password := s.down, s.password
	s.mu.Unlock()
	if down {
		hj, ok := w.(http.Hijacker)
		if ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close() // as a process that is not listening
				return
			}
		}
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	if r.URL.Path == "/health" {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"Unauthorized"}`))
		return
	}
	if r.URL.Query().Get("password") != password {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.connMu.Lock()
	s.conns[c] = &sync.Mutex{}
	s.connMu.Unlock()
	go s.read(c)
}

func (s *Server) send(c *websocket.Conn, msg map[string]any) {
	s.connMu.Lock()
	wmu := s.conns[c]
	s.connMu.Unlock()
	if wmu == nil {
		return
	}
	wmu.Lock()
	defer wmu.Unlock()
	_ = c.WriteJSON(msg)
}

// broadcast sends a message to all clients, as wssBroadcastMessage does.
func (s *Server) broadcast(method string, response any) {
	s.connMu.Lock()
	conns := slices.Collect(maps.Keys(s.conns))
	s.connMu.Unlock()
	for _, c := range conns {
		s.send(c, map[string]any{"id": 0, "src": "Matterbridge", "dst": "Frontend", "method": method, "success": true, "response": response})
	}
}

func (s *Server) snackbar(message string) {
	s.broadcast("snackbar", map[string]any{"message": message, "timeout": 5, "severity": "info"})
}

type request struct {
	ID     any            `json:"id"`
	Src    string         `json:"src"`
	Dst    string         `json:"dst"`
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

func (s *Server) read(c *websocket.Conn) {
	defer func() {
		s.connMu.Lock()
		delete(s.conns, c)
		s.connMu.Unlock()
		_ = c.Close()
	}()
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			return
		}
		var req request
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if err := dec.Decode(&req); err != nil {
			continue
		}
		go s.handle(c, req)
	}
}

func (s *Server) handle(c *websocket.Conn, req request) {
	answer := func(fields map[string]any) {
		msg := map[string]any{"id": req.ID, "method": req.Method, "src": "Matterbridge", "dst": req.Src}
		maps.Copy(msg, fields)
		s.send(c, msg)
	}
	fail := func(e string) { answer(map[string]any{"error": e}) }
	ok := func(response any) {
		if response == nil {
			answer(map[string]any{"success": true})
		} else {
			answer(map[string]any{"success": true, "response": response})
		}
	}
	if _, isNumber := req.ID.(json.Number); !isNumber || req.Dst != "Matterbridge" || req.Src == "" || req.Method == "" {
		fail("Invalid message")
		return
	}
	// Some noise, as the real frontend sends log lines to the clients.
	s.send(c, map[string]any{"id": 0, "src": "Matterbridge", "dst": "Frontend", "method": "log", "success": true, "response": map[string]any{"level": "debug", "message": "Received " + req.Method}})
	p := req.Params
	if p == nil {
		p = map[string]any{}
	}
	if result, handled := s.read_(req.Method, p); handled {
		if e, isError := result.(error); isError {
			fail(e.Error())
		} else {
			ok(result)
		}
		return
	}
	s.mu.Lock()
	key := req.Method
	if req.Method == "/api/config" {
		key += " " + str(p["name"])
	}
	s.calls[key]++
	s.mu.Unlock()
	switch req.Method {
	case "/api/config":
		s.config(p, ok, fail)
	case "/api/install", "/api/uninstall":
		pkg, isString := p["packageName"].(string)
		_, restartIsBool := p["restart"].(bool)
		if !isString || len(pkg) < 12 || (req.Method == "/api/install" && !restartIsBool) {
			fail("Wrong parameter in " + req.Method)
			return
		}
		ok(nil)
		go s.npm(strings.TrimPrefix(req.Method, "/api/"), pkg)
	case "/api/addplugin":
		name, _, _ := strings.Cut(str(p["pluginNameOrPath"]), "@") // as frontend.ts, which removes @version
		if err := s.addPlugin(name); err != nil {
			fail(err.Error())
			return
		}
		ok(nil)
	case "/api/removeplugin", "/api/enableplugin", "/api/disableplugin", "/api/restartplugin":
		if err := s.pluginCommand(req.Method, str(p["pluginName"])); err != nil {
			fail(err.Error())
			return
		}
		ok(nil)
	case "/api/savepluginconfig":
		name := str(p["pluginName"])
		formData, isObject := p["formData"].(map[string]any)
		s.mu.Lock()
		plugin := s.plugin(name)
		s.mu.Unlock()
		if plugin == nil {
			fail("Wrong parameter pluginName in /api/savepluginconfig")
			return
		}
		if !isObject || len(formData) < 5 {
			fail("Wrong parameter formData in /api/savepluginconfig")
			return
		}
		ok(nil)
		go func() {
			time.Sleep(s.opts.SaveDelay)
			s.mu.Lock()
			defer s.mu.Unlock()
			// saveConfigFromJson ignores a config without the correct name and type.
			if formData["name"] != plugin.Name || formData["type"] == nil || formData["type"] == "" {
				return
			}
			plugin.ConfigJSON = clone(formData).(map[string]any) // json.Number to float64
			plugin.RestartRequired = true
			s.info["restartRequired"] = true
		}()
	case "/api/restart":
		// restartProcess stops the process before it answers.
		go s.restart()
	case "/api/matter":
		s.matterCommand(p, ok, fail)
	default:
		fail("Unknown method " + req.Method)
	}
}

// read_ answers the requests that do not change anything.
func (s *Server) read_(method string, p map[string]any) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch method {
	case "ping":
		return "pong", true
	case "/api/settings":
		info := maps.Clone(s.info)
		info["startupAt"] = s.startupAt
		return map[string]any{"systemInformation": map[string]any{"hostname": "fake"}, "matterbridgeInformation": info}, true
	case "/api/plugins":
		return clone(s.plugins), true
	case "/api/devices":
		name := str(p["pluginName"])
		out := []any{}
		for _, d := range s.devices {
			if plugin := s.plugin(str(d["plugin"])); plugin == nil || !plugin.Started || plugin.RegisteredDevices == 0 {
				continue
			}
			if name == "" || d["plugin"] == name {
				out = append(out, d)
			}
		}
		return clone(out), true
	case "/api/select/devices":
		name := str(p["plugin"])
		if len(name) < 10 {
			return fmt.Errorf("Wrong parameter plugin in /api/select/devices"), true
		}
		if s.plugin(name) == nil {
			return fmt.Errorf("Plugin not found in /api/select/devices"), true
		}
		out := []any{}
		for _, d := range s.selectDevices {
			if d["plugin"] == name {
				out = append(out, d)
			}
		}
		return clone(out), true
	case "/api/matter":
		if len(p) == 1 && str(p["id"]) == "Matterbridge" {
			return clone(s.matter), true
		}
	}
	return nil, false
}

func (s *Server) matterCommand(p map[string]any, ok func(any), fail func(string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := str(p["id"])
	if id == "" {
		fail("Wrong parameter id in /api/matter")
		return
	}
	if id != "Matterbridge" {
		fail("Unknown server node id " + id + " in /api/matter")
		return
	}
	if p["startCommission"] == true {
		s.matter["advertising"], s.matter["windowStatus"], s.matter["advertiseTime"] = true, 2, time.Now().UnixMilli()
		s.calls["/api/matter startCommission"]++
	}
	if p["stopCommission"] == true {
		s.matter["advertising"], s.matter["windowStatus"], s.matter["advertiseTime"] = false, 0, 0
		s.calls["/api/matter stopCommission"]++
	}
	if p["advertise"] == true {
		s.matter["advertising"] = true
		s.calls["/api/matter advertise"]++
	}
	ok(clone(s.matter))
}

var logLevels = []string{"Debug", "Info", "Notice", "Warn", "Error", "Fatal"}

// config is /api/config. As in frontend.ts, some names do not answer if the value is incorrect.
func (s *Server) config(p map[string]any, ok func(any), fail func(string)) {
	name := str(p["name"])
	value, has := p["value"]
	if len(name) < 5 || !has {
		fail("Wrong parameter in /api/config")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	text, isString := value.(string)
	flag, isBool := value.(bool)
	restart := func() { s.info["restartRequired"] = true }
	number := func(lo, hi int) (int, bool) {
		if !isString {
			return 0, false
		}
		n, err := strconv.Atoi(text)
		return n, err == nil && n >= lo && n <= hi
	}
	switch name {
	case "setpassword":
		if isString {
			s.password = text
			ok(nil)
		}
	case "setbridgemode":
		if isString && (text == "bridge" || text == "childbridge") {
			s.stored["bridgeMode"] = text // only the node context: the running mode changes after a restart
			restart()
			ok(nil)
		}
	case "setmbloglevel", "setmjloglevel":
		if isString && len(text) >= 4 {
			key := map[string]string{"setmbloglevel": "loggerLevel", "setmjloglevel": "matterLoggerLevel"}[name]
			if slices.Contains(logLevels, text) {
				s.info[key] = strings.ToLower(text)
			}
			ok(nil)
		}
	case "setmblogfile", "setmjlogfile":
		if isBool {
			s.info[map[string]string{"setmblogfile": "fileLogger", "setmjlogfile": "matterFileLogger"}[name]] = flag
			ok(nil)
		}
	case "setmdnsinterface", "setipv4address", "setipv6address":
		if isString {
			key := map[string]string{"setmdnsinterface": "matterMdnsInterface", "setipv4address": "matterIpv4Address", "setipv6address": "matterIpv6Address"}[name]
			if text == "" {
				delete(s.info, key)
			} else {
				s.info[key] = text
			}
			restart()
			ok(nil)
		}
	case "setmatterport":
		restart()
		if port, valid := number(5540, 5600); valid {
			s.info["matterPort"] = port
			ok(nil)
		} else {
			s.info["matterPort"] = 5540
			fail("Invalid value: reset matter commissioning port to default 5540")
		}
	case "setmatterdiscriminator":
		restart()
		if d, valid := number(0, 4095); valid {
			s.info["matterDiscriminator"] = d
			ok(nil)
		} else {
			delete(s.info, "matterDiscriminator")
			fail("Invalid value: reset matter commissioning discriminator to default undefined")
		}
	case "setmatterpasscode":
		restart()
		if pc, valid := number(1, 99999998); valid && pc != 12345678 && pc != 11111111 {
			s.info["matterPasscode"] = pc
			ok(nil)
		} else {
			delete(s.info, "matterPasscode")
			fail("Invalid value: reset matter commissioning passcode to default undefined")
		}
	case "setvirtualmode":
		if isString && slices.Contains([]string{"disabled", "light", "outlet", "switch", "mounted_switch"}, text) {
			s.info["virtualMode"] = text
			restart()
		}
		ok(nil) // answers also if the value is incorrect
	default:
		fail("Unknown parameter " + name + " in /api/config")
	}
}

// npm runs npm install or uninstall. It tells the result with a snackbar, as manager_spawn_response does.
func (s *Server) npm(command, pkg string) {
	time.Sleep(s.opts.InstallDelay)
	name, version := pkg, "" // also for a scoped package, such as @scope/name@1.0.0
	if at := strings.LastIndex(pkg, "@"); at > 0 {
		name, version = pkg[:at], pkg[at+1:]
	}
	s.mu.Lock()
	versions, known := Registry[name]
	success := false
	switch {
	case command == "uninstall":
		_, success = s.installed[name]
		delete(s.installed, name)
	case !known:
	case version == "" || version == "latest":
		s.installed[name], success = versions[len(versions)-1], true
	case slices.Contains(versions, version):
		s.installed[name], success = version, true
	}
	if success {
		s.info["restartRequired"], s.info["fixedRestartRequired"] = true, true
	}
	s.mu.Unlock()
	if success {
		s.snackbar(map[string]string{"install": "Installed", "uninstall": "Uninstalled"}[command] + " package " + pkg)
	} else {
		s.snackbar("Package " + pkg + " not " + command + "ed")
	}
}

func (s *Server) addPlugin(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	version, installed := s.installed[name]
	if len(name) < 10 || !installed || s.plugin(name) != nil {
		return fmt.Errorf("Plugin %s not added", name)
	}
	s.plugins = append(s.plugins, &Plugin{
		Type: "DynamicPlatform", Name: name, Version: version, LatestVersion: Registry[name][len(Registry[name])-1], Enabled: true, Loaded: true,
		ConfigJSON: map[string]any{"name": name, "type": "DynamicPlatform", "version": version, "debug": false, "unregisterOnShutdown": false},
		SchemaJSON: map[string]any{"title": name, "type": "object", "properties": map[string]any{
			"name": map[string]any{"type": "string"}, "type": map[string]any{"type": "string"}, "debug": map[string]any{"type": "boolean"},
			"username": map[string]any{"type": "string"}, "password": map[string]any{"type": "string", "ui:widget": "password"},
		}},
	})
	s.info["restartRequired"] = true
	return nil
}

func (s *Server) pluginCommand(method, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	plugin := s.plugin(name)
	if len(name) < 10 || plugin == nil {
		return fmt.Errorf("Wrong parameter pluginName in %s", method)
	}
	switch method {
	case "/api/removeplugin":
		s.plugins = slices.DeleteFunc(s.plugins, func(p *Plugin) bool { return p == plugin })
	case "/api/enableplugin":
		plugin.Enabled, plugin.Loaded, plugin.Started, plugin.Configured = true, true, true, true
		plugin.RegisteredDevices = s.registered[name]
	case "/api/disableplugin":
		// It removes all the devices of the plugin, so a later enable can register them again.
		plugin.Enabled, plugin.Loaded, plugin.Started, plugin.Configured = false, false, false, false
		plugin.RegisteredDevices = 0
	case "/api/restartplugin":
		plugin.Loaded, plugin.Started, plugin.Configured, plugin.RestartRequired = true, true, true, false
		if s.info["bridgeMode"] == "bridge" {
			// Matterbridge 3.11 leaves the old endpoints in the aggregator, so the plugin cannot register its
			// devices again (frontend.ts /api/restartplugin, matterbridge.ts addBridgedEndpoint).
			plugin.RegisteredDevices = 0
		}
		if !slices.ContainsFunc(s.plugins, func(p *Plugin) bool { return p.RestartRequired }) {
			s.info["restartRequired"] = false
		}
	}
	return nil
}

// restart stops the server for RestartDelay, as restartProcess and the add-on supervisor do. The new process
// uses the stored settings and the installed plugin versions.
func (s *Server) restart() {
	s.mu.Lock()
	s.down = true
	s.mu.Unlock()
	s.connMu.Lock()
	conns := slices.Collect(maps.Keys(s.conns))
	s.connMu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	time.Sleep(s.opts.RestartDelay)
	s.mu.Lock()
	defer s.mu.Unlock()
	maps.Copy(s.info, s.stored)
	s.info["restartRequired"], s.info["fixedRestartRequired"] = false, false
	for _, p := range s.plugins {
		if v, ok := s.installed[p.Name]; ok {
			p.Version = v
			p.ConfigJSON["version"] = v
		} else {
			p.Error = true
		}
		p.RestartRequired = false
		p.Loaded, p.Started, p.Configured = p.Enabled, p.Enabled, p.Enabled
		p.RegisteredDevices = 0
		if p.Enabled {
			p.RegisteredDevices = s.registered[p.Name]
		}
	}
	s.startupAt = time.Now().UnixMilli()
	s.down = false
}

func (s *Server) plugin(name string) *Plugin {
	for _, p := range s.plugins {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// Do sends a request as the frontend does, to make changes that the provider does not know about. It waits for
// the effects of fire-and-forget requests.
func (s *Server) Do(method string, params map[string]any) error {
	ws := strings.Replace(s.URL, "http://", "ws://", 1) + "/?password=" + s.Password()
	c, _, err := websocket.DefaultDialer.Dial(ws, nil)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.WriteJSON(map[string]any{"id": 1, "src": "test", "dst": "Matterbridge", "method": method, "params": params}); err != nil {
		return err
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var msg map[string]any
		if err := c.ReadJSON(&msg); err != nil {
			return err
		}
		if msg["id"] == 1.0 {
			if e, has := msg["error"]; has {
				return fmt.Errorf("%v", e)
			}
			time.Sleep(2 * s.opts.SaveDelay)
			return nil
		}
	}
}

// DropConnections closes all WebSocket connections, as a network problem does. The server stays up.
func (s *Server) DropConnections() {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	for c := range s.conns {
		_ = c.Close()
	}
}

// SetBridgeMode changes the bridge mode at once, as a restart after setbridgemode does.
func (s *Server) SetBridgeMode(mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.info["bridgeMode"] = mode
}

// Plugin returns a copy of a plugin, or nil.
func (s *Server) Plugin(name string) *Plugin {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.plugin(name); p != nil {
		c := *p
		c.ConfigJSON = clone(p.ConfigJSON).(map[string]any)
		return &c
	}
	return nil
}

// Info returns a copy of matterbridgeInformation.
func (s *Server) Info() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.info).(map[string]any)
}

func (s *Server) Password() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.password
}

// Installed returns the version of a package on disk, or "".
func (s *Server) Installed(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.installed[name]
}

// Calls returns how many times each changing request came.
func (s *Server) Calls() Calls {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.calls)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// clone returns a deep copy through JSON. Numbers become float64, as for a client.
func clone(v any) any {
	data, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(data, &out)
	return out
}
