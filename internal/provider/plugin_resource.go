package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.ResourceWithConfigure      = &pluginResource{}
	_ resource.ResourceWithImportState    = &pluginResource{}
	_ resource.ResourceWithModifyPlan     = &pluginResource{}
	_ resource.ResourceWithValidateConfig = &pluginResource{}
)

// pluginResource is a Matterbridge plugin: its npm version, enabled and its config (the config editor of the
// frontend). config and sensitive_config manage only the top-level keys that they contain.
type pluginResource struct {
	resourceBase
}

type pluginModel struct {
	ID                types.String  `tfsdk:"id"`
	Name              types.String  `tfsdk:"name"`
	Version           types.String  `tfsdk:"version"`
	Enabled           types.Bool    `tfsdk:"enabled"`
	Config            types.Dynamic `tfsdk:"config"`
	SensitiveConfig   types.Map     `tfsdk:"sensitive_config"`
	Restart           types.String  `tfsdk:"restart"`
	RemoveOnDestroy   types.Bool    `tfsdk:"remove_on_destroy"`
	LatestVersion     types.String  `tfsdk:"latest_version"`
	RegisteredDevices types.Int64   `tfsdk:"registered_devices"`
}

// Matterbridge sets these keys of configJson itself.
var ownedConfigKeys = []string{"name", "type", "version"}

const (
	restartAuto         = "auto"
	restartPlugin       = "plugin"
	restartMatterbridge = "matterbridge"
	restartNone         = "none"
)

func NewPluginResource() resource.Resource { return &pluginResource{} }

func (r *pluginResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_plugin"
}

func (r *pluginResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A Matterbridge plugin. If Matterbridge does not have the plugin, the provider installs it with npm, adds it and restarts Matterbridge. " +
			"A change to version installs that version (also an older version) and restarts Matterbridge. " +
			"A change to the config saves the config and restarts Matterbridge or the plugin (see restart). " +
			"If you destroy the resource, the plugin does not change, unless remove_on_destroy is true.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, Description: "The plugin name.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required: true, Description: "The npm package, for example matterbridge-zigbee2mqtt. If you change it, OpenTofu replaces the resource.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"version": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "The npm version, for example 3.3.0. If it is different from the installed version, the apply installs it and restarts Matterbridge. " +
					"If you do not set it, the provider does not change the version, and the attribute shows the installed version.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "If false, the plugin and its devices stop. The default is true.",
			},
			"config": schema.DynamicAttribute{
				Optional: true,
				Description: "The plugin config (configJson) as an object, for example { topic = \"zigbee2mqtt\", whiteList = [\"Kitchen\"] }. " +
					"The provider manages only the top-level keys in this object. It compares each value as a whole, also lists and objects. " +
					"Other keys do not change. If you remove a key, the provider stops managing it, and the value does not change. " +
					"You cannot set name, type and version. Put secrets (password, token, and other settings that the plugin shows as passwords) in sensitive_config.",
			},
			"sensitive_config": schema.MapAttribute{
				Optional: true, Sensitive: true, ElementType: types.StringType,
				Description: "More config keys with string values, as for config, but OpenTofu does not show the values. For example { password = var.mqtt_password }. " +
					"A key cannot be in config and in sensitive_config. (It is a map of strings and not dynamic, because OpenTofu writes dynamic values to its debug log.)",
			},
			"restart": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString(restartAuto),
				Description: "What the apply restarts after a config change: auto (the default: matterbridge in bridge mode, plugin in childbridge mode), " +
					"plugin (only this plugin restarts, and the other plugins continue), matterbridge (all of Matterbridge), or none (the config applies at the next restart). " +
					"In bridge mode, Matterbridge 3.11 does not register the devices again after a plugin restart. Thus, if the plugin has no devices after the restart, " +
					"the provider restarts Matterbridge.",
				Validators: []validator.String{stringvalidator.OneOf(restartAuto, restartPlugin, restartMatterbridge, restartNone)},
			},
			"remove_on_destroy": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "If true, destroy removes the plugin from Matterbridge and uninstalls the npm package. Apply a change to this flag before you destroy the resource.",
			},
			"latest_version":     schema.StringAttribute{Computed: true, Description: "The latest version on npm, as Matterbridge shows it."},
			"registered_devices": schema.Int64Attribute{Computed: true, Description: "The number of devices that the plugin registered in Matterbridge."},
		},
	}
}

// managedConfig returns config and sensitive_config together. It returns errUnknown if a value is not known yet.
func (m *pluginModel) managedConfig() (config, sensitive map[string]any, err error) {
	if config, err = dynamicObject(m.Config); err != nil {
		return nil, nil, err
	}
	if sensitive, err = stringMap(m.SensitiveConfig); err != nil {
		return nil, nil, err
	}
	return config, sensitive, nil
}

func merged(config, sensitive map[string]any) map[string]any {
	out := map[string]any{}
	maps.Copy(out, config)
	maps.Copy(out, sensitive)
	return out
}

func (r *pluginResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m pluginModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	config, err := dynamicObject(m.Config)
	if err != nil && err != errUnknown {
		resp.Diagnostics.AddAttributeError(path.Root("config"), "Incorrect config", err.Error())
	}
	sensitive, err := stringMap(m.SensitiveConfig)
	if err != nil && err != errUnknown {
		resp.Diagnostics.AddAttributeError(path.Root("sensitive_config"), "Incorrect sensitive_config", err.Error())
	}
	for _, attr := range []struct {
		name   string
		values map[string]any
	}{{"config", config}, {"sensitive_config", sensitive}} {
		for _, k := range ownedConfigKeys {
			if _, has := attr.values[k]; has {
				resp.Diagnostics.AddAttributeError(path.Root(attr.name), "Key not allowed", attr.name+" cannot set "+k+". Matterbridge sets it.")
			}
		}
	}
	for _, k := range sortedKeys(config) {
		if _, has := sensitive[k]; has {
			resp.Diagnostics.AddAttributeError(path.Root("sensitive_config"), "Key in config and sensitive_config", k+" is in config and in sensitive_config. Put it in one of them.")
		}
		if isSecret(nil, k) {
			addSecretError(&resp.Diagnostics, k)
		}
	}
}

func (r *pluginResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	var plan pluginModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.Name.IsUnknown() {
		return
	}
	name := plan.Name.ValueString()
	p, err := r.client.Plugin(ctx, name)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the Matterbridge plugins", err.Error())
		return
	}
	if p == nil {
		resp.Diagnostics.AddWarning("This apply restarts Matterbridge",
			fmt.Sprintf("Matterbridge does not have %s. The apply installs it with npm, adds it and restarts Matterbridge. HomeKit and other controllers lose the devices for some time.", name))
		return
	}
	if !plan.Version.IsUnknown() && !plan.Version.IsNull() && plan.Version.ValueString() != p.Version {
		resp.Diagnostics.AddWarning("This apply restarts Matterbridge",
			fmt.Sprintf("The apply installs %s@%s (now %s) and restarts Matterbridge. HomeKit and other controllers lose the devices for some time.", name, plan.Version.ValueString(), p.Version))
	}
	config, sensitive, err := plan.managedConfig()
	if err != nil {
		return // unknown values: the apply checks again
	}
	for _, k := range sortedKeys(config) {
		// ValidateConfig already reported the fixed secret keys.
		if isSecret(p.SchemaJSON, k) && !isSecret(nil, k) {
			addSecretError(&resp.Diagnostics, k)
		}
	}
	managed := merged(config, sensitive)
	if !req.State.Raw.IsNull() {
		var state pluginModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if before, beforeSensitive, err := state.managedConfig(); err == nil {
			var dropped []string
			for _, k := range sortedKeys(merged(before, beforeSensitive)) {
				if _, has := managed[k]; !has {
					dropped = append(dropped, k)
				}
			}
			if len(dropped) > 0 {
				resp.Diagnostics.AddWarning("Config keys no longer managed",
					"The plan removes these keys from config or sensitive_config: "+strings.Join(dropped, ", ")+". "+
						"The provider stops managing them. Matterbridge keeps their values, and the apply does not send them.")
			}
		}
	}
	next := nextConfig(p, managed)
	root := func(k string) string {
		if _, has := sensitive[k]; has {
			return "sensitive_config"
		}
		if _, has := config[k]; has {
			return "config"
		}
		return "configJson"
	}
	// A problem in a key that the resource does not manage is only a warning: the configuration cannot fix it.
	var problems, unmanaged []string
	for _, problem := range validateSchema(next, p.SchemaJSON, root) {
		if _, has := managed[problem.Key]; has || problem.Key == "" {
			problems = append(problems, problem.Text)
		} else {
			unmanaged = append(unmanaged, problem.Text)
		}
	}
	if len(problems) > 0 {
		resp.Diagnostics.AddError("Config does not match the schema of "+name, "The schemaJson of the plugin does not accept the config:\n  "+strings.Join(problems, "\n  "))
	}
	if len(unmanaged) > 0 {
		resp.Diagnostics.AddWarning("Current config does not match the schema of "+name,
			"These keys are not in config or sensitive_config. Add them to fix them:\n  "+strings.Join(unmanaged, "\n  "))
	}
	if warnings := r.selectionWarnings(ctx, p, config); len(warnings) > 0 {
		resp.Diagnostics.AddAttributeWarning(path.Root("config"), "Unknown devices or features",
			name+" does not offer these devices or features now (/api/select/devices). Check the names. A device that is offline can also be missing:\n  "+strings.Join(warnings, "\n  "))
	}
	if !configMatches(p.ConfigJSON, managed) && plan.Enabled.ValueBool() && p.Enabled {
		restart, bridgeMode, err := r.restartMode(ctx, plan.Restart.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Cannot read the Matterbridge settings", err.Error())
			return
		}
		switch {
		case restart == restartPlugin && bridgeMode:
			resp.Diagnostics.AddWarning("restart = \"plugin\" in bridge mode",
				"The config of "+name+" changes, and the apply restarts the plugin. In bridge mode, Matterbridge 3.11 does not register the devices of the plugin again after a plugin restart, "+
					"and HomeKit and other controllers lose them. If the plugin has no devices after the restart, the apply restarts Matterbridge. Use restart = \"auto\" to restart Matterbridge at once.")
		case restart == restartPlugin:
			resp.Diagnostics.AddWarning("This apply restarts "+name, "The config changes. The apply restarts the plugin. Its devices are not available for some seconds. The other plugins continue.")
		case restart == restartMatterbridge:
			resp.Diagnostics.AddWarning("This apply restarts Matterbridge", "The config of "+name+" changes. HomeKit and other controllers lose the devices for some time.")
		}
	}
}

// restartMode returns the restart for a config change: auto becomes matterbridge in bridge mode and plugin in
// childbridge mode. bridgeMode is true if Matterbridge is in bridge mode.
func (r *pluginResource) restartMode(ctx context.Context, restart string) (string, bool, error) {
	if restart != restartAuto && restart != restartPlugin {
		return restart, false, nil
	}
	info, err := r.client.Settings(ctx)
	if err != nil {
		return "", false, err
	}
	bridgeMode := info["bridgeMode"] != "childbridge"
	if restart == restartAuto {
		restart = restartPlugin
		if bridgeMode {
			restart = restartMatterbridge
		}
	}
	return restart, bridgeMode, nil
}

// nextConfig returns the configJson to save: the current config with the managed keys. name and type must be
// correct, or Matterbridge ignores the config.
func nextConfig(p *Plugin, managed map[string]any) map[string]any {
	next := map[string]any{}
	maps.Copy(next, p.ConfigJSON)
	maps.Copy(next, managed)
	next["name"] = p.Name
	if t, _ := next["type"].(string); t == "" {
		next["type"] = p.Type
	}
	return next
}

// selectionWarnings returns the devices and features in whiteList, blackList and deviceFeatureBlackList that the
// plugin does not offer. Matterbridge plugins select devices by name or by serial number.
func (r *pluginResource) selectionWarnings(ctx context.Context, p *Plugin, config map[string]any) []string {
	_, hasWhite := config["whiteList"]
	_, hasBlack := config["blackList"]
	features, hasFeatures := config["deviceFeatureBlackList"].(map[string]any)
	if (!hasWhite && !hasBlack && !hasFeatures) || !p.Started {
		return nil
	}
	devices, err := r.client.SelectDevices(ctx, p.Name)
	if err != nil || len(devices) == 0 {
		return nil
	}
	find := func(name string) *SelectDevice {
		for i := range devices {
			if devices[i].DeviceName == name || devices[i].SerialNumber == name {
				return &devices[i]
			}
		}
		return nil
	}
	var warnings []string
	for _, list := range []string{"whiteList", "blackList"} {
		entries, _ := config[list].([]any)
		for _, e := range entries {
			if name, ok := e.(string); ok && find(name) == nil {
				warnings = append(warnings, fmt.Sprintf("%s: %q", list, name))
			}
		}
	}
	for _, name := range sortedKeys(features) {
		d := find(name)
		if d == nil {
			warnings = append(warnings, fmt.Sprintf("deviceFeatureBlackList: %q", name))
			continue
		}
		list, _ := features[name].([]any)
		for _, f := range list {
			feature, _ := f.(string)
			if len(d.Entities) > 0 && !slices.ContainsFunc(d.Entities, func(e Entity) bool { return e.Name == feature }) {
				warnings = append(warnings, fmt.Sprintf("deviceFeatureBlackList[%q]: feature %q", name, feature))
			}
		}
	}
	return warnings
}

// apply makes Matterbridge match plan.
func (r *pluginResource) apply(ctx context.Context, plan *pluginModel, state *tfsdk.State, diags *diag.Diagnostics) {
	defer r.client.Lock()()
	name := plan.Name.ValueString()
	p, err := r.client.Plugin(ctx, name)
	if err != nil {
		diags.AddError("Cannot read the Matterbridge plugins", err.Error())
		return
	}
	want := ""
	if !plan.Version.IsNull() && !plan.Version.IsUnknown() {
		want = plan.Version.ValueString()
	}
	fullRestart := false
	if p == nil || (want != "" && want != p.Version) {
		pkg := name
		if want != "" {
			pkg += "@" + want
		}
		if err := r.client.Install(ctx, pkg); err != nil {
			diags.AddError("Cannot install "+pkg, err.Error())
			return
		}
		if p == nil {
			if err := r.client.Call(ctx, "/api/addplugin", map[string]any{"pluginNameOrPath": name}, nil); err != nil {
				diags.AddError("Cannot add "+name, err.Error())
				return
			}
			if p, err = r.client.Plugin(ctx, name); err != nil || p == nil {
				diags.AddError("Cannot add "+name, fmt.Sprintf("Matterbridge does not show the plugin after /api/addplugin (%v).", err))
				return
			}
		}
		fullRestart = true
	}

	config, sensitive, err := plan.managedConfig()
	if err != nil {
		diags.AddError("Incorrect config", err.Error())
		return
	}
	managed := merged(config, sensitive)
	configChanged := !configMatches(p.ConfigJSON, managed)
	wasEnabled, devicesBefore := p.Enabled, p.RegisteredDevices
	if configChanged {
		if err := r.client.Call(ctx, "/api/savepluginconfig", map[string]any{"pluginName": name, "formData": nextConfig(p, managed)}, nil); err != nil {
			diags.AddError("Cannot save the config of "+name, err.Error())
			return
		}
		// Matterbridge saves the config after it answers.
		if p, err = r.waitConfig(ctx, name, managed); err != nil {
			diags.AddError("Cannot save the config of "+name, err.Error())
			return
		}
	}
	// p is current, unless a later step changes the plugin.
	stale := fullRestart
	enabled := plan.Enabled.ValueBool()
	if enabled != wasEnabled {
		stale = true
		method := "/api/disableplugin"
		if enabled {
			method = "/api/enableplugin" // it loads the plugin with the new config
		}
		if err := r.client.Call(ctx, method, map[string]any{"pluginName": name}, nil); err != nil {
			diags.AddError("Cannot change enabled of "+name, err.Error())
			return
		}
	}
	// A plugin that was disabled, or that enable loaded just now, already runs with the new config.
	needsRestart := configChanged && enabled && wasEnabled
	restart := plan.Restart.ValueString()
	if needsRestart {
		if restart, _, err = r.restartMode(ctx, restart); err != nil {
			diags.AddError("Cannot read the Matterbridge settings", err.Error())
			return
		}
	}
	switch {
	case fullRestart || (needsRestart && restart == restartMatterbridge):
		stale = true
		if err := r.client.Restart(ctx); err != nil {
			diags.AddError("Matterbridge restart failed", err.Error())
			return
		}
	case needsRestart && restart == restartPlugin:
		stale = true
		if err := r.client.Call(ctx, "/api/restartplugin", map[string]any{"pluginName": name}, nil); err != nil {
			diags.AddError("Cannot restart "+name, "The config is saved, but the plugin did not restart: "+err.Error())
			return
		}
		if devicesBefore > 0 && !r.waitRegistered(ctx, name) {
			diags.AddWarning("Matterbridge restarted",
				name+" registered no devices after the plugin restart (in bridge mode, Matterbridge 3.11 does not register them again). The apply restarted Matterbridge.")
			if err := r.client.Restart(ctx); err != nil {
				diags.AddError("Matterbridge restart failed", err.Error())
				return
			}
		}
	case needsRestart:
		diags.AddWarning("Restart required", "The config of "+name+" is saved. It applies when the plugin or Matterbridge restarts.")
	}

	if stale {
		if p, err = r.client.Plugin(ctx, name); err != nil || p == nil {
			diags.AddError("Cannot read "+name, fmt.Sprintf("Matterbridge does not show the plugin (%v).", err))
			return
		}
	}
	if want != "" && p.Version != want {
		diags.AddError("Version not installed", fmt.Sprintf("Matterbridge shows %s %s after the install of %s.", name, p.Version, want))
		return
	}
	plan.ID = types.StringValue(name)
	plan.Version = types.StringValue(p.Version)
	fillPluginStatus(plan, p)
	diags.Append(state.Set(ctx, plan)...)
}

// waitConfig waits until configJson has the managed values, and returns the plugin.
func (r *pluginResource) waitConfig(ctx context.Context, name string, managed map[string]any) (*Plugin, error) {
	timeout := time.After(r.client.cfg.Timeout)
	for {
		p, err := r.client.Plugin(ctx, name)
		if err != nil {
			return nil, err
		}
		if p != nil && configMatches(p.ConfigJSON, managed) {
			return p, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout:
			return nil, fmt.Errorf("Matterbridge did not save the config in %s. The Matterbridge log can show why", r.client.cfg.Timeout)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// waitRegistered waits until the plugin has registered devices. It returns false if it has none after the timeout.
func (r *pluginResource) waitRegistered(ctx context.Context, name string) bool {
	timeout := time.After(r.client.cfg.Timeout)
	for {
		if p, err := r.client.Plugin(ctx, name); err == nil && p != nil && p.RegisteredDevices > 0 {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-timeout:
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func addSecretError(diags *diag.Diagnostics, key string) {
	diags.AddAttributeError(path.Root("config"), "Secret in config", key+" is a secret. Put it in sensitive_config, so that OpenTofu does not show it.")
}

func fillPluginStatus(m *pluginModel, p *Plugin) {
	m.LatestVersion = types.StringValue(p.LatestVersion)
	m.RegisteredDevices = types.Int64Value(p.RegisteredDevices)
}

func (r *pluginResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan pluginModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if !resp.Diagnostics.HasError() {
		r.apply(ctx, &plan, &resp.State, &resp.Diagnostics)
	}
}

func (r *pluginResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan pluginModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if !resp.Diagnostics.HasError() {
		r.apply(ctx, &plan, &resp.State, &resp.Diagnostics)
	}
}

// readManaged returns the current values of the keys that v manages. It keeps null if v is null.
func readManaged(ctx context.Context, v types.Dynamic, current map[string]any) types.Dynamic {
	managed, err := dynamicObject(v)
	if err != nil || managed == nil {
		return v
	}
	return dynamicFromJSON(ctx, filterKeys(current, managed), v)
}

// stringMap returns sensitive_config as a JSON-like map. It returns nil if the attribute is null.
func stringMap(v types.Map) (map[string]any, error) {
	if v.IsNull() {
		return nil, nil
	}
	if v.IsUnknown() {
		return nil, errUnknown
	}
	out := map[string]any{}
	for k, e := range v.Elements() {
		s, ok := e.(types.String)
		if !ok || s.IsUnknown() {
			return nil, errUnknown
		}
		if !s.IsNull() {
			out[k] = s.ValueString()
		}
	}
	return out, nil
}

// readSensitive returns the current values of the keys that sensitive_config manages. A value that is not a
// string becomes its JSON text, so that the plan shows a change.
func readSensitive(v types.Map, current map[string]any) types.Map {
	managed, err := stringMap(v)
	if err != nil || managed == nil {
		return v
	}
	values := map[string]attr.Value{}
	for k, value := range filterKeys(current, managed) {
		if s, ok := value.(string); ok {
			values[k] = types.StringValue(s)
		} else {
			data, _ := json.Marshal(value)
			values[k] = types.StringValue(string(data))
		}
	}
	return types.MapValueMust(types.StringType, values)
}

func (r *pluginResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m pluginModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.client.Plugin(ctx, m.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the Matterbridge plugins", err.Error())
		return
	}
	if p == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	m.ID = types.StringValue(p.Name)
	m.Version = types.StringValue(p.Version)
	m.Enabled = types.BoolValue(p.Enabled)
	m.Config = readManaged(ctx, m.Config, p.ConfigJSON)
	m.SensitiveConfig = readSensitive(m.SensitiveConfig, p.ConfigJSON)
	fillPluginStatus(&m, p)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *pluginResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m pluginModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := m.Name.ValueString()
	if !m.RemoveOnDestroy.ValueBool() {
		resp.Diagnostics.AddWarning("Plugin not changed", "OpenTofu no longer manages "+name+". The plugin and its config do not change. To remove a plugin, set remove_on_destroy = true before you destroy it.")
		return
	}
	defer r.client.Lock()()
	p, err := r.client.Plugin(ctx, name)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the Matterbridge plugins", err.Error())
		return
	}
	if p != nil {
		if err := r.client.Call(ctx, "/api/removeplugin", map[string]any{"pluginName": name}, nil); err != nil {
			resp.Diagnostics.AddError("Cannot remove "+name, err.Error())
			return
		}
	}
	if err := r.client.Uninstall(ctx, name); err != nil {
		resp.Diagnostics.AddWarning("npm uninstall failed", "Matterbridge no longer has "+name+", but the npm package is still installed: "+err.Error())
	}
}

// ImportState accepts the plugin name. The state gets all of configJson (without name, type and version). The
// secrets go to sensitive_config. Thus, the plan is empty if the configuration has the same keys and values. A
// secret that is not a string cannot be in sensitive_config, and must not be in config, so it is not managed.
func (r *pluginResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	p, err := r.client.Plugin(ctx, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the Matterbridge plugins", err.Error())
		return
	}
	if p == nil {
		resp.Diagnostics.AddError("Plugin not found", "Matterbridge does not have the plugin "+req.ID+". Use the npm package name, for example matterbridge-zigbee2mqtt.")
		return
	}
	config, sensitive := map[string]any{}, map[string]attr.Value{}
	for k, v := range p.ConfigJSON {
		switch {
		case v == nil || slices.Contains(ownedConfigKeys, k):
		case isSecret(p.SchemaJSON, k):
			if s, ok := v.(string); ok {
				sensitive[k] = types.StringValue(s)
			}
		default:
			config[k] = v
		}
	}
	m := pluginModel{
		ID: types.StringValue(p.Name), Name: types.StringValue(p.Name), Version: types.StringValue(p.Version), Enabled: types.BoolValue(p.Enabled),
		Config: types.DynamicNull(), SensitiveConfig: types.MapNull(types.StringType),
		Restart: types.StringValue(restartAuto), RemoveOnDestroy: types.BoolValue(false),
	}
	if len(config) > 0 {
		m.Config = types.DynamicValue(jsonToAttr(ctx, config))
	}
	if len(sensitive) > 0 {
		m.SensitiveConfig = types.MapValueMust(types.StringType, sensitive)
	}
	fillPluginStatus(&m, p)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}
