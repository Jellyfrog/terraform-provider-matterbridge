package fakematterbridge

import (
	"testing"
	"time"
)

// The acceptance tests depend on these transitions of the fake, which copy Matterbridge 3.11.
func TestLifecycle(t *testing.T) {
	s, err := Start(Options{RestartDelay: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	do := func(method string, params map[string]any) {
		t.Helper()
		if err := s.Do(method, params); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
	}
	restart := func() {
		t.Helper()
		s.mu.Lock()
		before := s.startupAt
		s.mu.Unlock()
		_ = s.Do("/api/restart", nil) // no answer: the process stops
		for range 100 {
			time.Sleep(10 * time.Millisecond)
			s.mu.Lock()
			up := !s.down && s.startupAt != before
			s.mu.Unlock()
			if up {
				return
			}
		}
		t.Fatal("the fake did not start again")
	}
	devices := func(plugin string) int {
		t.Helper()
		result, _ := s.read_("/api/devices", map[string]any{"pluginName": plugin})
		return len(result.([]any))
	}
	z2m := func() *Plugin { return s.Plugin(Zigbee2MQTT) }

	if z2m().RegisteredDevices != 29 || devices(Zigbee2MQTT) != 5 {
		t.Fatalf("start: %d registered, %d listed", z2m().RegisteredDevices, devices(Zigbee2MQTT))
	}

	// Bridge mode: a plugin restart loses the devices until Matterbridge restarts.
	do("/api/restartplugin", map[string]any{"pluginName": Zigbee2MQTT})
	if p := z2m(); !p.Started || p.RegisteredDevices != 0 || devices(Zigbee2MQTT) != 0 {
		t.Fatalf("after restartplugin in bridge mode: %+v, %d listed", p.RegisteredDevices, devices(Zigbee2MQTT))
	}
	restart()
	if z2m().RegisteredDevices != 29 || devices(Zigbee2MQTT) != 5 {
		t.Fatal("restart did not register the devices again")
	}

	// Disable removes the devices, enable registers them again.
	do("/api/disableplugin", map[string]any{"pluginName": Zigbee2MQTT})
	if p := z2m(); p.Enabled || p.Started || p.RegisteredDevices != 0 {
		t.Fatalf("after disable: %+v", p)
	}
	do("/api/enableplugin", map[string]any{"pluginName": Zigbee2MQTT})
	if p := z2m(); !p.Started || p.RegisteredDevices != 29 {
		t.Fatalf("after enable: %+v", p)
	}

	// setbridgemode changes only the stored value. The running mode changes at the restart.
	do("/api/config", map[string]any{"name": "setbridgemode", "value": "childbridge"})
	if s.Info()["bridgeMode"] != "bridge" || s.Info()["restartRequired"] != true {
		t.Fatalf("before restart: %v", s.Info())
	}
	restart()
	if s.Info()["bridgeMode"] != "childbridge" || s.Info()["restartRequired"] != false {
		t.Fatalf("after restart: %v", s.Info())
	}

	// Childbridge mode: a plugin restart keeps the devices.
	do("/api/restartplugin", map[string]any{"pluginName": Zigbee2MQTT})
	if z2m().RegisteredDevices != 29 {
		t.Fatal("restartplugin in childbridge mode lost the devices")
	}

	// A new version on disk shows only after a restart. A disabled plugin stays without devices.
	if err := s.Do("/api/install", map[string]any{"packageName": Hass + "@1.5.0", "restart": false}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // npm
	do("/api/disableplugin", map[string]any{"pluginName": Hass})
	if s.Plugin(Hass).Version != "1.6.0" || s.Info()["fixedRestartRequired"] != true {
		t.Fatalf("before restart: hass %s", s.Plugin(Hass).Version)
	}
	restart()
	if p := s.Plugin(Hass); p.Version != "1.5.0" || p.RegisteredDevices != 0 || p.Started {
		t.Fatalf("after restart: %+v", p)
	}
}
