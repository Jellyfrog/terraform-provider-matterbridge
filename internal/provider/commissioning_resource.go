package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.ResourceWithConfigure = &commissioningResource{}

// commissioningResource sends a commissioning command to a Matter server node. It sends the command only on
// create and when trigger changes. A refresh never sends it.
type commissioningResource struct {
	resourceBase
}

type commissioningModel struct {
	ID           types.String `tfsdk:"id"`
	Node         types.String `tfsdk:"node"`
	Action       types.String `tfsdk:"action"`
	Trigger      types.String `tfsdk:"trigger"`
	Advertising  types.Bool   `tfsdk:"advertising"`
	WindowStatus types.Int64  `tfsdk:"window_status"`
}

const defaultNode = "Matterbridge"

// The /api/matter parameter for each action.
var commissioningCommands = map[string]string{"open": "startCommission", "close": "stopCommission", "advertise": "advertise"}

func NewCommissioningResource() resource.Resource { return &commissioningResource{} }

func (r *commissioningResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_commissioning"
}

func (r *commissioningResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Sends a commissioning command to a Matter server node, as the buttons of the frontend do. " +
			"The apply sends the command when it creates the resource and when trigger changes. A plan or a refresh never sends it. " +
			"Destroy does nothing. The resource cannot remove a fabric.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "The node.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"node": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString(defaultNode),
				Description:   "The server node: Matterbridge (the default, bridge mode), or a plugin name in childbridge mode.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"action": schema.StringAttribute{
				Required: true,
				Description: "open (open the commissioning window, so that a new controller can pair), close (close the window), " +
					"or advertise (announce the node again with mDNS). A change to action alone does not send the command.",
				Validators: []validator.String{stringvalidator.OneOf("open", "close", "advertise")},
			},
			"trigger": schema.StringAttribute{
				Required:    true,
				Description: "Change this value to send the command again, for example a date.",
			},
			"advertising":   schema.BoolAttribute{Computed: true, Description: "True if the node advertised after the last command."},
			"window_status": schema.Int64Attribute{Computed: true, Description: "The commissioning window after the last command: 0 closed, 1 enhanced, 2 basic."},
		},
	}
}

func (r *commissioningResource) run(ctx context.Context, m *commissioningModel, diagsAdd func(string, string)) bool {
	defer r.client.Lock()()
	info, err := r.client.MatterCommand(ctx, m.Node.ValueString(), commissioningCommands[m.Action.ValueString()])
	if err != nil {
		diagsAdd("Commissioning command failed", err.Error())
		return false
	}
	m.ID = m.Node
	m.Advertising, m.WindowStatus = types.BoolValue(info.Advertising), types.Int64Value(info.WindowStatus)
	return true
}

func (r *commissioningResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m commissioningModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if !resp.Diagnostics.HasError() && r.run(ctx, &m, resp.Diagnostics.AddError) {
		resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
	}
}

// Read does not send a command, and does not read the node: the computed values show the result of the last command.
func (r *commissioningResource) Read(context.Context, resource.ReadRequest, *resource.ReadResponse) {}

func (r *commissioningResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state commissioningModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.Trigger.Equal(state.Trigger) {
		plan.Advertising, plan.WindowStatus = state.Advertising, state.WindowStatus
	} else if !r.run(ctx, &plan, resp.Diagnostics.AddError) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *commissioningResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}
