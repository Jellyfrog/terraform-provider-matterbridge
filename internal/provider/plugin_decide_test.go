package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jellyfrog/terraform-provider-matterbridge/internal/fakematterbridge"
)

func TestDecide(t *testing.T) {
	server := startFake(t, fakematterbridge.Options{})
	r := &pluginResource{resourceBase{client: testClient(t, server)}}
	ctx := context.Background()
	z2m, err := r.client.Plugin(ctx, fakematterbridge.Zigbee2MQTT)
	if err != nil {
		t.Fatal(err)
	}
	newWhiteList := map[string]any{"whiteList": []any{"Office Ceiling"}}
	sameTopic := map[string]any{"topic": "zigbee2mqtt"}
	model := func(name, version, restart string, enabled bool) *pluginModel {
		m := &pluginModel{Name: types.StringValue(name), Version: types.StringNull(), Enabled: types.BoolValue(enabled), Restart: types.StringValue(restart)}
		if version != "" {
			m.Version = types.StringValue(version)
		}
		return m
	}
	tests := []struct {
		name       string
		bridgeMode string
		plugin     *Plugin
		plan       *pluginModel
		managed    map[string]any
		want       pluginChanges
	}{
		{"no change", "bridge", z2m, model(z2m.Name, "3.3.0", restartAuto, true), sameTopic, pluginChanges{}},
		{"new plugin", "bridge", nil, model(fakematterbridge.Shelly, "2.0.0", restartAuto, true), sameTopic,
			pluginChanges{Install: fakematterbridge.Shelly + "@2.0.0", Add: true, SaveConfig: true, Restart: restartAll}},
		{"new plugin, disabled", "bridge", nil, model(fakematterbridge.Shelly, "", restartAuto, false), nil,
			pluginChanges{Install: fakematterbridge.Shelly, Add: true, SetEnabled: true, Restart: restartAll}},
		{"version", "bridge", z2m, model(z2m.Name, "3.2.0", restartPlugin, true), newWhiteList,
			pluginChanges{Install: z2m.Name + "@3.2.0", SaveConfig: true, Restart: restartAll}},
		{"config, auto, bridge", "bridge", z2m, model(z2m.Name, "", restartAuto, true), newWhiteList,
			pluginChanges{SaveConfig: true, Restart: restartAll, BridgeMode: true}},
		{"config, auto, childbridge", "childbridge", z2m, model(z2m.Name, "", restartAuto, true), newWhiteList,
			pluginChanges{SaveConfig: true, Restart: restartThePlugin}},
		{"config, plugin, bridge", "bridge", z2m, model(z2m.Name, "", restartPlugin, true), newWhiteList,
			pluginChanges{SaveConfig: true, Restart: restartThePlugin, BridgeMode: true}},
		{"config, none", "bridge", z2m, model(z2m.Name, "", restartNone, true), newWhiteList,
			pluginChanges{SaveConfig: true, Restart: restartLater}},
		{"config and disable: no restart", "bridge", z2m, model(z2m.Name, "", restartAuto, false), newWhiteList,
			pluginChanges{SaveConfig: true, SetEnabled: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server.SetBridgeMode(tt.bridgeMode)
			got, err := r.decide(ctx, tt.plugin, tt.plan, tt.managed)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
			if summary, _ := got.warning(tt.plan.Name.ValueString(), "3.3.0"); (summary == "") != (got.Restart == restartNothing || got.Restart == restartLater) {
				t.Errorf("warning %q for restart %d", summary, got.Restart)
			}
		})
	}
}
