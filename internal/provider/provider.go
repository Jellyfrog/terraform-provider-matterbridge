package provider

import (
	"context"
	"os"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type matterbridgeProvider struct {
	version string
}

type providerModel struct {
	URL            types.String `tfsdk:"url"`
	Password       types.String `tfsdk:"password"`
	OldPassword    types.String `tfsdk:"old_password"`
	Timeout        types.Int64  `tfsdk:"timeout"`
	RestartTimeout types.Int64  `tfsdk:"restart_timeout"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider { return &matterbridgeProvider{version: version} }
}

func (p *matterbridgeProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "matterbridge"
	resp.Version = p.version
}

func (p *matterbridgeProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages Matterbridge through the WebSocket API of its frontend: settings, plugins and their config. " +
			"Some changes restart Matterbridge. During a restart, HomeKit and other Matter controllers lose the devices for some time.",
		Attributes: map[string]schema.Attribute{
			"url": schema.StringAttribute{
				Optional:    true,
				Description: "The Matterbridge frontend, for example http://192.168.1.10:8283. Schemes: http, https, ws, wss. The default is MATTERBRIDGE_URL.",
			},
			"password": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "The frontend password. The default is MATTERBRIDGE_PASSWORD. If Matterbridge has no password, the provider connects without one. " +
					"Thus, you can set the password that matterbridge_settings sets.",
			},
			"old_password": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "The password before a change. The provider tries it if Matterbridge does not accept password. The default is MATTERBRIDGE_OLD_PASSWORD.",
			},
			"timeout": schema.Int64Attribute{
				Optional:    true,
				Validators:  []validator.Int64{int64validator.AtLeast(1)},
				Description: "The number of seconds to wait for each answer. The default is 30.",
			},
			"restart_timeout": schema.Int64Attribute{
				Optional:    true,
				Validators:  []validator.Int64{int64validator.AtLeast(1)},
				Description: "The number of seconds to wait for npm and for a restart of Matterbridge with its plugins. The default is 300.",
			},
		},
	}
}

// setting returns the value of the attribute, or else the value of the environment variable.
func setting(v types.String, env string) string {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueString()
	}
	return os.Getenv(env)
}

// seconds returns the duration, or 0 (the default of the client) if the value is not set.
func seconds(v types.Int64) time.Duration {
	return time.Duration(v.ValueInt64()) * time.Second
}

func (p *matterbridgeProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	u := setting(config.URL, "MATTERBRIDGE_URL")
	if u == "" {
		resp.Diagnostics.AddAttributeError(path.Root("url"), "Missing url", "Set url or MATTERBRIDGE_URL to the Matterbridge frontend, for example http://192.168.1.10:8283.")
		return
	}
	cfg := ClientConfig{
		URL:            u,
		Timeout:        seconds(config.Timeout),
		RestartTimeout: seconds(config.RestartTimeout),
	}
	for _, pw := range []string{setting(config.Password, "MATTERBRIDGE_PASSWORD"), setting(config.OldPassword, "MATTERBRIDGE_OLD_PASSWORD")} {
		if pw != "" {
			cfg.Passwords = append(cfg.Passwords, pw)
		}
	}
	// The client connects when it is first used. Thus, validate does not need Matterbridge.
	client, err := NewClient(cfg)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("url"), "Incorrect url", err.Error()+". The value is "+u+".")
		return
	}
	resp.ResourceData = client
	resp.DataSourceData = client
}

func (p *matterbridgeProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{NewSettingsResource, NewPluginResource, NewCommissioningResource}
}

func (p *matterbridgeProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{NewPluginsDataSource, NewDevicesDataSource, NewSelectDevicesDataSource, NewMatterDataSource}
}
