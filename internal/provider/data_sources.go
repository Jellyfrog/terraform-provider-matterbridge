package provider

import (
	"context"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSourceWithConfigure = &pluginsDataSource{}
	_ datasource.DataSourceWithConfigure = &devicesDataSource{}
	_ datasource.DataSourceWithConfigure = &selectDevicesDataSource{}
	_ datasource.DataSourceWithConfigure = &matterDataSource{}
)

// dataSource holds the client, which all the data sources share.
type dataSource struct {
	client *Client
}

func (d *dataSource) Configure(_ context.Context, req datasource.ConfigureRequest, _ *datasource.ConfigureResponse) {
	if req.ProviderData != nil {
		d.client = req.ProviderData.(*Client)
	}
}

func computed(attrs map[string]schema.Attribute) schema.ListNestedAttribute {
	return schema.ListNestedAttribute{Computed: true, NestedObject: schema.NestedAttributeObject{Attributes: attrs}}
}

func str(description string) schema.StringAttribute {
	return schema.StringAttribute{Computed: true, Description: description}
}

func boolean(description string) schema.BoolAttribute {
	return schema.BoolAttribute{Computed: true, Description: description}
}

func integer(description string) schema.Int64Attribute {
	return schema.Int64Attribute{Computed: true, Description: description}
}

// matterbridge_plugins

type pluginsDataSource struct{ dataSource }

type pluginData struct {
	Name              string `tfsdk:"name"`
	Version           string `tfsdk:"version"`
	LatestVersion     string `tfsdk:"latest_version"`
	Type              string `tfsdk:"type"`
	Description       string `tfsdk:"description"`
	Enabled           bool   `tfsdk:"enabled"`
	Loaded            bool   `tfsdk:"loaded"`
	Started           bool   `tfsdk:"started"`
	Configured        bool   `tfsdk:"configured"`
	Error             bool   `tfsdk:"error"`
	RestartRequired   bool   `tfsdk:"restart_required"`
	RegisteredDevices int64  `tfsdk:"registered_devices"`
}

type pluginsData struct {
	Plugins []pluginData `tfsdk:"plugins"`
}

func NewPluginsDataSource() datasource.DataSource { return &pluginsDataSource{} }

func (d *pluginsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_plugins"
}

func (d *pluginsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The plugins of Matterbridge and their status. The config is not here, because it can contain secrets. Use matterbridge_plugin for the config.",
		Attributes: map[string]schema.Attribute{
			"plugins": computed(map[string]schema.Attribute{
				"name": str("The npm package."), "version": str("The installed version."), "latest_version": str("The latest version on npm."),
				"type": str("DynamicPlatform or AccessoryPlatform."), "description": str("The description."),
				"enabled": boolean(""), "loaded": boolean(""), "started": boolean(""), "configured": boolean(""),
				"error":              boolean("True if the plugin had an error."),
				"restart_required":   boolean("True if the plugin has changes that apply after a restart."),
				"registered_devices": integer("The number of devices that the plugin registered."),
			}),
		},
	}
}

func (d *pluginsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	plugins, err := d.client.Plugins(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the Matterbridge plugins", err.Error())
		return
	}
	out := pluginsData{Plugins: []pluginData{}}
	for _, p := range plugins {
		out.Plugins = append(out.Plugins, pluginData{
			Name: p.Name, Version: p.Version, LatestVersion: p.LatestVersion, Type: p.Type, Description: p.Description,
			Enabled: p.Enabled, Loaded: p.Loaded, Started: p.Started, Configured: p.Configured, Error: p.Error,
			RestartRequired: p.RestartRequired, RegisteredDevices: p.RegisteredDevices,
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, out)...)
}

// matterbridge_devices

type devicesDataSource struct{ dataSource }

type deviceData struct {
	Plugin       string `tfsdk:"plugin"`
	Name         string `tfsdk:"name"`
	SerialNumber string `tfsdk:"serial_number"`
	UniqueID     string `tfsdk:"unique_id"`
	ID           string `tfsdk:"id"`
	Endpoint     int64  `tfsdk:"endpoint"`
	Type         string `tfsdk:"type"`
	Reachable    bool   `tfsdk:"reachable"`
	PowerSource  string `tfsdk:"power_source"`
	ConfigURL    string `tfsdk:"config_url"`
}

type devicesData struct {
	Plugin  types.String `tfsdk:"plugin"`
	Devices []deviceData `tfsdk:"devices"`
}

func NewDevicesDataSource() datasource.DataSource { return &devicesDataSource{} }

func (d *devicesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_devices"
}

func (d *devicesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The devices that the plugins registered in Matterbridge (the Matter devices that the controllers see).",
		Attributes: map[string]schema.Attribute{
			"plugin": schema.StringAttribute{Optional: true, Description: "Only the devices of this plugin. If you do not set it, the devices of all plugins."},
			"devices": computed(map[string]schema.Attribute{
				"plugin": str("The plugin."), "name": str("The device name."), "serial_number": str("The serial number, for example the IEEE address for zigbee2mqtt."),
				"unique_id": str("The unique ID in Matterbridge."), "id": str("The endpoint ID."), "endpoint": integer("The endpoint number."),
				"type": str("The Matter device type, for example DimmableLight (0x0101)."), "reachable": boolean("True if the device is reachable."),
				"power_source": str("ac, dc, ok, warning, critical or \"\"."), "config_url": str("The configuration URL of the device."),
			}),
		},
	}
}

func (d *devicesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m devicesData
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("plugin"), &m.Plugin)...)
	if resp.Diagnostics.HasError() {
		return
	}
	devices, err := d.client.Devices(ctx, m.Plugin.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the Matterbridge devices", err.Error())
		return
	}
	m.Devices = []deviceData{}
	for _, dev := range devices {
		out := deviceData{
			Plugin: dev.Plugin, Name: dev.DeviceName, SerialNumber: dev.SerialNumber, UniqueID: dev.UniqueID, Type: dev.Type,
			Reachable: dev.Reachable, PowerSource: dev.PowerSource, ConfigURL: dev.ConfigURL,
		}
		if dev.ID != nil {
			out.ID = *dev.ID
		}
		if dev.Number != nil {
			out.Endpoint = *dev.Number
		}
		m.Devices = append(m.Devices, out)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// matterbridge_select_devices

type selectDevicesDataSource struct{ dataSource }

type entityData struct {
	Name        string `tfsdk:"name"`
	Description string `tfsdk:"description"`
}

type selectDeviceData struct {
	Name         string       `tfsdk:"name"`
	SerialNumber string       `tfsdk:"serial_number"`
	Entities     []entityData `tfsdk:"entities"`
	Selected     bool         `tfsdk:"selected"`
	Registered   bool         `tfsdk:"registered"`
}

type selectDevicesData struct {
	Plugin  types.String       `tfsdk:"plugin"`
	Names   []string           `tfsdk:"names"`
	Devices []selectDeviceData `tfsdk:"devices"`
}

func NewSelectDevicesDataSource() datasource.DataSource { return &selectDevicesDataSource{} }

func (d *selectDevicesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_select_devices"
}

func (d *selectDevicesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The devices that a plugin offers for its whiteList, blackList and deviceFeatureBlackList (the device list of the config editor).",
		Attributes: map[string]schema.Attribute{
			"plugin": schema.StringAttribute{Required: true, Description: "The plugin, for example matterbridge-zigbee2mqtt."},
			"names":  schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "The device names, for a contains() check."},
			"devices": computed(map[string]schema.Attribute{
				"name": str("The device name. The lists of the config use it."), "serial_number": str("The serial number."),
				"entities": computed(map[string]schema.Attribute{
					"name": str("The feature, for example device_temperature. deviceFeatureBlackList uses it."), "description": str(""),
				}),
				"selected":   boolean("True if the current whiteList and blackList let the plugin expose the device. An empty whiteList selects all devices."),
				"registered": boolean("True if the device is registered in Matterbridge now (matterbridge_devices)."),
			}),
		},
	}
}

func (d *selectDevicesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m selectDevicesData
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("plugin"), &m.Plugin)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := m.Plugin.ValueString()
	fail := func(err error) { resp.Diagnostics.AddError("Cannot read the devices of "+name, err.Error()) }
	p, err := d.client.Plugin(ctx, name)
	if err != nil {
		fail(err)
		return
	}
	if p == nil {
		resp.Diagnostics.AddAttributeError(path.Root("plugin"), "Plugin not found", "Matterbridge does not have the plugin "+name+".")
		return
	}
	selectable, err := d.client.SelectDevices(ctx, name)
	if err != nil {
		fail(err)
		return
	}
	registered, err := d.client.Devices(ctx, name)
	if err != nil {
		fail(err)
		return
	}
	white, _ := p.ConfigJSON["whiteList"].([]any)
	black, _ := p.ConfigJSON["blackList"].([]any)
	in := func(list []any, dev SelectDevice) bool {
		return slices.Contains(list, any(dev.DeviceName)) || slices.Contains(list, any(dev.SerialNumber))
	}
	m.Names, m.Devices = []string{}, []selectDeviceData{}
	for _, dev := range selectable {
		out := selectDeviceData{
			Name: dev.DeviceName, SerialNumber: dev.SerialNumber, Entities: []entityData{},
			Selected: (len(white) == 0 || in(white, dev)) && !in(black, dev),
			Registered: slices.ContainsFunc(registered, func(r Device) bool {
				return r.SerialNumber == dev.SerialNumber || r.DeviceName == dev.DeviceName
			}),
		}
		for _, e := range dev.Entities {
			out.Entities = append(out.Entities, entityData{Name: e.Name, Description: e.Description})
		}
		m.Names = append(m.Names, dev.DeviceName)
		m.Devices = append(m.Devices, out)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// matterbridge_matter

type matterDataSource struct{ dataSource }

type fabricData struct {
	Index      int64  `tfsdk:"index"`
	FabricID   string `tfsdk:"fabric_id"`
	NodeID     string `tfsdk:"node_id"`
	VendorID   int64  `tfsdk:"vendor_id"`
	VendorName string `tfsdk:"vendor_name"`
	Label      string `tfsdk:"label"`
}

type matterData struct {
	Node              types.String `tfsdk:"node"`
	Online            bool         `tfsdk:"online"`
	Commissioned      bool         `tfsdk:"commissioned"`
	Advertising       bool         `tfsdk:"advertising"`
	WindowStatus      int64        `tfsdk:"window_status"`
	SerialNumber      string       `tfsdk:"serial_number"`
	SessionCount      int64        `tfsdk:"session_count"`
	Fabrics           []fabricData `tfsdk:"fabrics"`
	QRPairingCode     types.String `tfsdk:"qr_pairing_code"`
	ManualPairingCode types.String `tfsdk:"manual_pairing_code"`
}

func NewMatterDataSource() datasource.DataSource { return &matterDataSource{} }

func (d *matterDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_matter"
}

func (d *matterDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The commissioning status of a Matter server node: the controllers (fabrics) that it is paired with, and the pairing codes. The data source only reads.",
		Attributes: map[string]schema.Attribute{
			"node": schema.StringAttribute{
				Optional:    true,
				Description: "The server node: Matterbridge (the default, bridge mode), or a plugin name in childbridge mode.",
			},
			"online":        boolean("True if the node is online."),
			"commissioned":  boolean("True if at least one controller is paired."),
			"advertising":   boolean("True if the node advertises for commissioning now."),
			"window_status": integer("The commissioning window: 0 closed, 1 enhanced window open, 2 basic window open."),
			"serial_number": str("The serial number of the node."),
			"session_count": integer("The number of sessions."),
			"fabrics": computed(map[string]schema.Attribute{
				"index": integer("The fabric index. It identifies the fabric on this node."), "fabric_id": str(""), "node_id": str("The node ID in the fabric."),
				"vendor_id": integer("The vendor ID of the controller, for example 4937 for Apple Home."), "vendor_name": str("For example (AppleHome)."),
				"label": str("The fabric label, for example the name of the home."),
			}),
			"qr_pairing_code":     schema.StringAttribute{Computed: true, Sensitive: true, Description: "The QR code content (MT:...)."},
			"manual_pairing_code": schema.StringAttribute{Computed: true, Sensitive: true, Description: "The manual pairing code."},
		},
	}
}

func (d *matterDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m matterData
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("node"), &m.Node)...)
	if resp.Diagnostics.HasError() {
		return
	}
	node := m.Node.ValueString()
	if node == "" {
		node = defaultNode
	}
	info, err := d.client.Matter(ctx, node)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the Matter node "+node, err.Error())
		return
	}
	m.Node = types.StringValue(node)
	m.Online, m.Commissioned, m.Advertising, m.WindowStatus = info.Online, info.Commissioned, info.Advertising, info.WindowStatus
	m.SerialNumber, m.SessionCount = info.SerialNumber, int64(len(info.Sessions))
	m.QRPairingCode, m.ManualPairingCode = types.StringValue(info.QRPairingCode), types.StringValue(info.ManualPairingCode)
	m.Fabrics = []fabricData{}
	for _, f := range info.Fabrics {
		m.Fabrics = append(m.Fabrics, fabricData{Index: f.FabricIndex, FabricID: f.FabricID, NodeID: f.NodeID, VendorID: f.RootVendorID, VendorName: f.RootVendorName, Label: f.Label})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}
