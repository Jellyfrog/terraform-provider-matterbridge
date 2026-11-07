package provider

import (
	"context"
	"fmt"
)

// pluginRestart is what the apply restarts after it changed a plugin.
type pluginRestart int

const (
	// restartNothing: nothing changed that needs a restart.
	restartNothing pluginRestart = iota
	// restartLater: the config changes, but restart is "none". The config applies at the next restart.
	restartLater
	// restartThePlugin: /api/restartplugin. In bridge mode, a fallback to restartAll can follow.
	restartThePlugin
	// restartAll: /api/restart, all of Matterbridge.
	restartAll
)

// pluginChanges is what the apply does to make Matterbridge match the plan, in this sequence: install, add,
// save the config, enable or disable, restart. ModifyPlan shows warnings from the same value, so the plan and
// the apply cannot disagree.
type pluginChanges struct {
	// Install is the npm package to install ("name" or "name@version"), or "".
	Install string
	// Add is true if Matterbridge does not have the plugin. The apply adds it after the install.
	Add bool
	// SaveConfig is true if the managed keys are different from configJson. A new plugin always saves.
	SaveConfig bool
	// SetEnabled is true if enabled changes.
	SetEnabled bool
	Restart    pluginRestart
	// BridgeMode is true if Matterbridge is in bridge mode. There, a plugin restart can lose the devices.
	BridgeMode bool
}

// decide returns the changes from p (nil if Matterbridge does not have the plugin) to the plan.
func (r *pluginResource) decide(ctx context.Context, p *Plugin, plan *pluginModel, managed map[string]any) (pluginChanges, error) {
	name, enabled := plan.Name.ValueString(), plan.Enabled.ValueBool()
	want := ""
	if !plan.Version.IsNull() && !plan.Version.IsUnknown() {
		want = plan.Version.ValueString()
	}
	var c pluginChanges
	if p == nil {
		c.Install, c.Add, c.Restart = name, true, restartAll
		if want != "" {
			c.Install += "@" + want
		}
		c.SaveConfig = len(managed) > 0
		c.SetEnabled = !enabled // Matterbridge adds a plugin enabled
		return c, nil
	}
	c.SaveConfig = !configMatches(p.ConfigJSON, managed)
	c.SetEnabled = enabled != p.Enabled
	if want != "" && want != p.Version {
		// Matterbridge loads a new version only when it starts.
		c.Install, c.Restart = name+"@"+want, restartAll
		return c, nil
	}
	// A plugin that is disabled, or that enable loads, runs with the new config without a restart.
	if !c.SaveConfig || !enabled || !p.Enabled {
		return c, nil
	}
	restart := plan.Restart.ValueString()
	if restart == restartNone {
		c.Restart = restartLater
		return c, nil
	}
	if restart == restartMatterbridge {
		c.Restart = restartAll
		return c, nil
	}
	info, err := r.client.Settings(ctx)
	if err != nil {
		return c, err
	}
	c.BridgeMode = info["bridgeMode"] != "childbridge"
	c.Restart = restartThePlugin
	if restart == restartAuto && c.BridgeMode {
		c.Restart = restartAll
	}
	return c, nil
}

// warning returns the plan warning for the restart, or "" if nothing restarts.
func (c pluginChanges) warning(name, version string) (summary, detail string) {
	const lose = " HomeKit and other controllers lose the devices for some time."
	switch {
	case c.Add:
		return "This apply restarts Matterbridge", fmt.Sprintf("Matterbridge does not have %s. The apply installs it with npm, adds it and restarts Matterbridge.%s", name, lose)
	case c.Install != "":
		return "This apply restarts Matterbridge", fmt.Sprintf("The apply installs %s (now %s) and restarts Matterbridge.%s", c.Install, version, lose)
	case c.Restart == restartAll:
		return "This apply restarts Matterbridge", "The config of " + name + " changes." + lose
	case c.Restart == restartThePlugin && c.BridgeMode:
		return "restart = \"plugin\" in bridge mode",
			"The config of " + name + " changes, and the apply restarts the plugin. In bridge mode, Matterbridge 3.11 does not register the devices of the plugin again after a plugin restart, " +
				"and HomeKit and other controllers lose them. If the plugin has no devices after the restart, the apply restarts Matterbridge. Use restart = \"auto\" to restart Matterbridge at once."
	case c.Restart == restartThePlugin:
		return "This apply restarts " + name, "The config changes. The apply restarts the plugin. Its devices are not available for some seconds. The other plugins continue."
	}
	return "", ""
}
