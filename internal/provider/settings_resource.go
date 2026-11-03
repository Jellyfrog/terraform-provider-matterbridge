package provider

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.ResourceWithConfigure      = &settingsResource{}
	_ resource.ResourceWithImportState    = &settingsResource{}
	_ resource.ResourceWithModifyPlan     = &settingsResource{}
	_ resource.ResourceWithValidateConfig = &settingsResource{}
)

// settingsResource is the Matterbridge settings, which the frontend changes with /api/config. There is one for
// each Matterbridge.
type settingsResource struct {
	resourceBase
}

type settingsModel struct {
	ID                  types.String `tfsdk:"id"`
	BridgeMode          types.String `tfsdk:"bridge_mode"`
	MatterPort          types.Int64  `tfsdk:"matter_port"`
	MatterPasscode      types.Int64  `tfsdk:"matter_passcode"`
	MatterDiscriminator types.Int64  `tfsdk:"matter_discriminator"`
	MdnsInterface       types.String `tfsdk:"mdns_interface"`
	IPv4Address         types.String `tfsdk:"ipv4_address"`
	IPv6Address         types.String `tfsdk:"ipv6_address"`
	VirtualMode         types.String `tfsdk:"virtual_mode"`
	MBLogLevel          types.String `tfsdk:"mb_log_level"`
	MJLogLevel          types.String `tfsdk:"mj_log_level"`
	MBLogFile           types.Bool   `tfsdk:"mb_log_file"`
	MJLogFile           types.Bool   `tfsdk:"mj_log_file"`
	PasswordWO          types.String `tfsdk:"password_wo"`
	PasswordWOVersion   types.String `tfsdk:"password_wo_version"`
	MatterbridgeVersion types.String `tfsdk:"matterbridge_version"`
	RestartRequired     types.Bool   `tfsdk:"restart_required"`
}

const settingsID = "matterbridge"

// settingSpec connects an attribute to a key of matterbridgeInformation and to an /api/config name.
type settingSpec struct {
	attr    string
	info    string
	command string
	// level is true for a log level: Matterbridge wants it capitalized and shows it in lower case.
	level bool
	// restart is true if Matterbridge uses the new value only after a restart.
	restart bool
	field   func(m *settingsModel) any // *types.String, *types.Int64 or *types.Bool
}

var settingSpecs = []settingSpec{
	{"bridge_mode", "bridgeMode", "setbridgemode", false, true, func(m *settingsModel) any { return &m.BridgeMode }},
	{"matter_port", "matterPort", "setmatterport", false, true, func(m *settingsModel) any { return &m.MatterPort }},
	{"matter_passcode", "matterPasscode", "setmatterpasscode", false, true, func(m *settingsModel) any { return &m.MatterPasscode }},
	{"matter_discriminator", "matterDiscriminator", "setmatterdiscriminator", false, true, func(m *settingsModel) any { return &m.MatterDiscriminator }},
	{"mdns_interface", "matterMdnsInterface", "setmdnsinterface", false, true, func(m *settingsModel) any { return &m.MdnsInterface }},
	{"ipv4_address", "matterIpv4Address", "setipv4address", false, true, func(m *settingsModel) any { return &m.IPv4Address }},
	{"ipv6_address", "matterIpv6Address", "setipv6address", false, true, func(m *settingsModel) any { return &m.IPv6Address }},
	{"virtual_mode", "virtualMode", "setvirtualmode", false, true, func(m *settingsModel) any { return &m.VirtualMode }},
	{"mb_log_level", "loggerLevel", "setmbloglevel", true, false, func(m *settingsModel) any { return &m.MBLogLevel }},
	{"mj_log_level", "matterLoggerLevel", "setmjloglevel", true, false, func(m *settingsModel) any { return &m.MJLogLevel }},
	{"mb_log_file", "fileLogger", "setmblogfile", false, false, func(m *settingsModel) any { return &m.MBLogFile }},
	{"mj_log_file", "matterFileLogger", "setmjlogfile", false, false, func(m *settingsModel) any { return &m.MJLogFile }},
}

var (
	logLevels    = []string{"debug", "info", "notice", "warn", "error", "fatal"}
	virtualModes = []string{"disabled", "light", "outlet", "switch", "mounted_switch"}
	// The Matter specification does not allow these passcodes.
	forbiddenPasscodes = []int64{0, 11111111, 22222222, 33333333, 44444444, 55555555, 66666666, 77777777, 88888888, 99999999, 12345678, 87654321}
)

// wire returns the value to send with /api/config, or nil if the value is null or unknown. Numbers are strings,
// because Matterbridge parses them from strings. Log levels are capitalized.
func (s settingSpec) wire(m *settingsModel) any {
	switch v := s.field(m).(type) {
	case *types.String:
		if v.IsNull() || v.IsUnknown() {
			return nil
		}
		if s.level {
			l := v.ValueString()
			return strings.ToUpper(l[:1]) + l[1:]
		}
		return v.ValueString()
	case *types.Int64:
		if v.IsNull() || v.IsUnknown() {
			return nil
		}
		return strconv.FormatInt(v.ValueInt64(), 10)
	case *types.Bool:
		if v.IsNull() || v.IsUnknown() {
			return nil
		}
		return v.ValueBool()
	}
	return nil
}

// current returns the value that Matterbridge has, in the form of wire.
func (s settingSpec) current(info map[string]any) any {
	m := &settingsModel{}
	s.set(m, info)
	return s.wire(m)
}

// set copies the value of matterbridgeInformation to the model. Matterbridge leaves out a value that is not set.
func (s settingSpec) set(m *settingsModel, info map[string]any) {
	value, has := info[s.info]
	switch f := s.field(m).(type) {
	case *types.String:
		text, _ := value.(string)
		if s.level {
			text = strings.ToLower(text)
			if !has {
				*f = types.StringNull()
				return
			}
		}
		*f = types.StringValue(text)
	case *types.Int64:
		if n, ok := value.(float64); ok {
			*f = types.Int64Value(int64(n))
		} else {
			*f = types.Int64Null()
		}
	case *types.Bool:
		b, _ := value.(bool)
		*f = types.BoolValue(b)
	}
}

func NewSettingsResource() resource.Resource { return &settingsResource{} }

func (r *settingsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_settings"
}

func optionalString(description string, validators ...validator.String) schema.StringAttribute {
	return schema.StringAttribute{
		Optional: true, Computed: true, Description: description, Validators: validators,
		PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
}

func optionalInt(description string, validators ...validator.Int64) schema.Int64Attribute {
	return schema.Int64Attribute{
		Optional: true, Computed: true, Description: description, Validators: validators,
		PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
	}
}

func optionalBool(description string) schema.BoolAttribute {
	return schema.BoolAttribute{
		Optional: true, Computed: true, Description: description,
		PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
	}
}

const restartNote = " A change restarts Matterbridge."

func (r *settingsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The Matterbridge settings, as on the settings page of the frontend. The resource manages only the attributes that you set. " +
			"The other attributes show the current values. If a change needs a restart, the apply restarts Matterbridge, and the plan shows a warning. " +
			"During the restart, HomeKit and other controllers lose the devices for some time. If you destroy the resource, the settings do not change.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, Description: "The value is always \"" + settingsID + "\".",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"bridge_mode": optionalString("bridge (one Matter node for all devices) or childbridge (one node for each plugin)."+restartNote,
				stringvalidator.OneOf("bridge", "childbridge")),
			"matter_port":          optionalInt("The Matter port, 5540–5600."+restartNote, int64validator.Between(5540, 5600)),
			"matter_passcode":      optionalInt("The commissioning passcode, 1–99999998."+restartNote, int64validator.Between(1, 99999998), int64validator.NoneOf(forbiddenPasscodes...)),
			"matter_discriminator": optionalInt("The commissioning discriminator, 0–4095."+restartNote, int64validator.Between(0, 4095)),
			"mdns_interface":       optionalString("The network interface for mDNS. \"\" means all interfaces." + restartNote),
			"ipv4_address":         optionalString("The IPv4 address that Matter uses. \"\" means all addresses." + restartNote),
			"ipv6_address":         optionalString("The IPv6 address that Matter uses. \"\" means all addresses." + restartNote),
			"virtual_mode": optionalString("The virtual devices of Matterbridge (restart, update and so on): disabled, light, outlet, switch or mounted_switch."+restartNote,
				stringvalidator.OneOf(virtualModes...)),
			"mb_log_level": optionalString("The Matterbridge log level: debug, info, notice, warn, error or fatal.", stringvalidator.OneOf(logLevels...)),
			"mj_log_level": optionalString("The matter.js log level: debug, info, notice, warn, error or fatal.", stringvalidator.OneOf(logLevels...)),
			"mb_log_file":  optionalBool("If true, Matterbridge also logs to a file."),
			"mj_log_file":  optionalBool("If true, matter.js also logs to a file."),
			"password_wo": schema.StringAttribute{
				Optional: true, Sensitive: true, WriteOnly: true,
				Description: "The frontend password. It is write-only, so it is not in the plan or the state. The provider sets it when password_wo_version changes. " +
					"\"\" removes the password. After the change, the provider uses the new password.",
			},
			"password_wo_version": schema.StringAttribute{
				Optional:    true,
				Description: "Change this value to set password_wo again. The provider cannot read the password, so it cannot see a difference.",
			},
			"matterbridge_version": schema.StringAttribute{
				Computed: true, Description: "The Matterbridge version. The Home Assistant add-on (or the image) controls it.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"restart_required": schema.BoolAttribute{Computed: true, Description: "True if Matterbridge has changes that apply only after a restart."},
		},
	}
}

func (r *settingsResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m settingsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !m.PasswordWO.IsNull() && m.PasswordWOVersion.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("password_wo_version"), "Missing password_wo_version",
			"Set password_wo_version with password_wo. The provider sets the password when password_wo_version changes.")
	}
}

// restartChanges returns the attributes in plan that need a restart and that are different from Matterbridge.
func restartChanges(plan *settingsModel, info map[string]any) []string {
	var changed []string
	for _, s := range settingSpecs {
		if v := s.wire(plan); s.restart && v != nil && !jsonEqual(v, s.current(info)) {
			changed = append(changed, s.attr)
		}
	}
	return changed
}

func (r *settingsResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	var plan settingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	info, err := r.client.Settings(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the Matterbridge settings", err.Error())
		return
	}
	if changed := restartChanges(&plan, info); len(changed) > 0 {
		resp.Diagnostics.AddWarning("This apply restarts Matterbridge",
			fmt.Sprintf("These settings apply only after a restart: %s. The apply restarts Matterbridge. HomeKit and other controllers lose the devices for some time.", strings.Join(changed, ", ")))
	}
}

// apply sends the changes. prior is the state before (nil on create). config is the configuration, which has
// the write-only password.
func (r *settingsResource) apply(ctx context.Context, plan, prior *settingsModel, config tfsdk.Config, state *tfsdk.State, diags *diag.Diagnostics) {
	defer r.client.Lock()()
	info, err := r.client.Settings(ctx)
	if err != nil {
		diags.AddError("Cannot read the Matterbridge settings", err.Error())
		return
	}
	changed, restart := false, false
	for _, s := range settingSpecs {
		v := s.wire(plan)
		if v == nil || jsonEqual(v, s.current(info)) {
			continue
		}
		if err := r.client.SetConfig(ctx, s.command, v); err != nil {
			diags.AddAttributeError(path.Root(s.attr), "Cannot change "+s.attr, err.Error())
			return
		}
		changed, restart = true, restart || s.restart
	}
	versionChanged := !plan.PasswordWOVersion.IsNull() && (prior == nil || !plan.PasswordWOVersion.Equal(prior.PasswordWOVersion))
	if versionChanged {
		var password types.String
		diags.Append(config.GetAttribute(ctx, path.Root("password_wo"), &password)...)
		if diags.HasError() {
			return
		}
		if password.IsNull() || password.IsUnknown() {
			diags.AddAttributeError(path.Root("password_wo"), "Missing password_wo", "password_wo_version changed, but password_wo is not set.")
			return
		}
		if err := r.client.SetPassword(ctx, password.ValueString()); err != nil {
			diags.AddAttributeError(path.Root("password_wo"), "Cannot change the frontend password", err.Error())
			return
		}
	}
	if restart {
		if err := r.client.Restart(ctx); err != nil {
			diags.AddError("Matterbridge restart failed", "The settings are saved, but Matterbridge did not start again: "+err.Error())
			return
		}
	}
	// The settings that the provider read first are still current if it sent nothing (a password is not in them).
	if changed {
		if info, err = r.client.Settings(ctx); err != nil {
			diags.AddError("Cannot read the Matterbridge settings", err.Error())
			return
		}
	}
	fillSettings(plan, info)
	diags.Append(state.Set(ctx, plan)...)
}

func fillSettings(m *settingsModel, info map[string]any) {
	m.ID = types.StringValue(settingsID)
	for _, s := range settingSpecs {
		s.set(m, info)
	}
	version, _ := info["matterbridgeVersion"].(string)
	restart, _ := info["restartRequired"].(bool)
	m.MatterbridgeVersion, m.RestartRequired = types.StringValue(version), types.BoolValue(restart)
	m.PasswordWO = types.StringNull() // write-only values are never in the state
}

func (r *settingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan settingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if !resp.Diagnostics.HasError() {
		r.apply(ctx, &plan, nil, req.Config, &resp.State, &resp.Diagnostics)
	}
}

func (r *settingsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m settingsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	info, err := r.client.Settings(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the Matterbridge settings", err.Error())
		return
	}
	fillSettings(&m, info)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *settingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state settingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if !resp.Diagnostics.HasError() {
		r.apply(ctx, &plan, &state, req.Config, &resp.State, &resp.Diagnostics)
	}
}

func (r *settingsResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning("Settings not changed", "If you remove matterbridge_settings, OpenTofu no longer manages the settings. The settings do not change.")
}

// ImportState accepts the ID "matterbridge". The refresh reads all the settings.
func (r *settingsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID != settingsID {
		resp.Diagnostics.AddError("Incorrect import ID", "Import matterbridge_settings with the ID \""+settingsID+"\".")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), settingsID)...)
}
