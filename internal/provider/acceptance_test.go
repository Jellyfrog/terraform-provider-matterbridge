package provider

import (
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/jellyfrog/terraform-provider-matterbridge/internal/fakematterbridge"
)

// The acceptance tests run OpenTofu (the version set in mise.toml) with a fake Matterbridge in this process.
// Thus, they do not need Matterbridge:
//
//	TF_ACC=1 go test ./internal/provider -run TestAcc -v
//
// The default of TF_ACC_TERRAFORM_PATH is `mise which tofu`. The default provider address is registry.opentofu.org/jellyfrog.
func accServer(t *testing.T, opts fakematterbridge.Options) *fakematterbridge.Server {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run acceptance tests")
	}
	if os.Getenv("TF_ACC_TERRAFORM_PATH") == "" {
		out, err := exec.Command("mise", "which", "tofu").Output()
		if err != nil {
			t.Fatalf("cannot find tofu with mise (run `mise install`), or set TF_ACC_TERRAFORM_PATH: %v", err)
		}
		t.Setenv("TF_ACC_TERRAFORM_PATH", strings.TrimSpace(string(out)))
	}
	if os.Getenv("TF_ACC_PROVIDER_HOST") == "" {
		t.Setenv("TF_ACC_PROVIDER_HOST", "registry.opentofu.org")
	}
	if os.Getenv("TF_ACC_PROVIDER_NAMESPACE") == "" {
		t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "jellyfrog")
	}
	return startFake(t, opts)
}

var accFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"matterbridge": providerserver.NewProtocol6WithError(New("test")()),
}

func accProvider(server *fakematterbridge.Server, password string) string {
	return fmt.Sprintf(`
provider "matterbridge" {
  url             = %q
  password        = %q
  timeout         = 5
  restart_timeout = 30
}
`, server.URL, password)
}

// serverCheck runs a check on the fake Matterbridge after the apply.
func serverCheck(f func() error) resource.TestCheckFunc {
	return func(*terraform.State) error { return f() }
}

func expectInfo(server *fakematterbridge.Server, key string, want any) resource.TestCheckFunc {
	return serverCheck(func() error {
		if got := server.Info()[key]; !reflect.DeepEqual(got, want) {
			return fmt.Errorf("matterbridgeInformation.%s: got %v, want %v", key, got, want)
		}
		return nil
	})
}

func expectCalls(server *fakematterbridge.Server, method string, want int) resource.TestCheckFunc {
	return serverCheck(func() error {
		if got := server.Calls()[method]; got != want {
			return fmt.Errorf("%s: got %d calls, want %d (all: %v)", method, got, want, server.Calls())
		}
		return nil
	})
}

func expectPluginConfig(server *fakematterbridge.Server, plugin, key string, want any) resource.TestCheckFunc {
	return serverCheck(func() error {
		p := server.Plugin(plugin)
		if p == nil {
			return fmt.Errorf("no plugin %s", plugin)
		}
		if got := p.ConfigJSON[key]; !reflect.DeepEqual(got, want) {
			return fmt.Errorf("%s config %s: got %#v, want %#v", plugin, key, got, want)
		}
		return nil
	})
}

// stepCalls counts the changing requests of one step. Call reset in PreConfig.
type stepCalls struct {
	server *fakematterbridge.Server
	before fakematterbridge.Calls
}

func (c *stepCalls) reset() { c.before = c.server.Calls() }

// expect checks the calls of the step. A method that is not in want must have no calls.
func (c *stepCalls) expect(want map[string]int) resource.TestCheckFunc {
	return serverCheck(func() error {
		got := map[string]int{}
		for method, n := range c.server.Calls() {
			if d := n - c.before[method]; d != 0 {
				got[method] = d
			}
		}
		if !reflect.DeepEqual(got, want) {
			return fmt.Errorf("calls of the step: got %v, want %v", got, want)
		}
		return nil
	})
}

func mustDo(t *testing.T, server *fakematterbridge.Server, method string, params map[string]any) {
	t.Helper()
	if err := server.Do(method, params); err != nil {
		t.Fatal(err)
	}
}

func TestAccSettings(t *testing.T) {
	server := accServer(t, fakematterbridge.Options{})
	step := &stepCalls{server: server}
	config := func(password, body string) string {
		return accProvider(server, password) + `
resource "matterbridge_settings" "this" {
` + body + `
}`
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: accFactories,
		Steps: []resource.TestStep{
			{
				Config:      config("", `bridge_mode = "hub"`),
				ExpectError: regexp.MustCompile(`bridge_mode`),
			},
			{
				Config:      config("", `matter_passcode = 12345678`),
				ExpectError: regexp.MustCompile(`matter_passcode`),
			},
			{
				Config:      config("", `password_wo = "x"`),
				ExpectError: regexp.MustCompile(`Missing password_wo_version`),
			},
			{
				// Only the log level: no restart.
				Config: config("", `mb_log_level = "debug"`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matterbridge_settings.this", "id", "matterbridge"),
					resource.TestCheckResourceAttr("matterbridge_settings.this", "bridge_mode", "bridge"),
					resource.TestCheckResourceAttr("matterbridge_settings.this", "matter_port", "5540"),
					resource.TestCheckResourceAttr("matterbridge_settings.this", "matterbridge_version", "3.11.0"),
					expectInfo(server, "loggerLevel", "debug"),
					expectCalls(server, "/api/config setmbloglevel", 1),
					expectCalls(server, "/api/restart", 0),
				),
			},
			{
				// The same configuration: an empty plan.
				Config:   config("", `mb_log_level = "debug"`),
				PlanOnly: true,
			},
			{
				// A setting that has its current value sends nothing, also with a new attribute.
				PreConfig: step.reset,
				Config:    config("", `mb_log_level = "debug"`+"\n"+`matter_port = 5540`),
				Check:     step.expect(map[string]int{}),
			},
			{
				Config: config("", `
  mb_log_level = "debug"
  bridge_mode  = "childbridge"
  matter_port  = 5541
  virtual_mode = "disabled"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("matterbridge_settings.this", plancheck.ResourceActionUpdate)}},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matterbridge_settings.this", "bridge_mode", "childbridge"),
					resource.TestCheckResourceAttr("matterbridge_settings.this", "restart_required", "false"),
					expectInfo(server, "bridgeMode", "childbridge"),
					expectInfo(server, "matterPort", 5541.0),
					expectCalls(server, "/api/restart", 1), // one restart for the three settings
					expectCalls(server, "/api/config setmbloglevel", 1),
				),
			},
			{
				// The provider password is the new password. It works before the change (Matterbridge has no
				// password, so the provider uses "") and after it.
				Config: config("s3cret-pw", `
  mb_log_level        = "debug"
  bridge_mode         = "childbridge"
  password_wo         = "s3cret-pw"
  password_wo_version = "1"`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckNoResourceAttr("matterbridge_settings.this", "password_wo"),
					serverCheck(func() error {
						if server.Password() != "s3cret-pw" {
							return fmt.Errorf("password not set")
						}
						return nil
					}),
				),
			},
			{
				// A person changes the log level in the frontend. The plan changes it back.
				PreConfig: func() { mustDo(t, server, "/api/config", map[string]any{"name": "setmbloglevel", "value": "Warn"}) },
				Config: config("s3cret-pw", `
  mb_log_level        = "debug"
  bridge_mode         = "childbridge"
  password_wo         = "s3cret-pw"
  password_wo_version = "1"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("matterbridge_settings.this", plancheck.ResourceActionUpdate)}},
				Check: resource.ComposeTestCheckFunc(
					expectInfo(server, "loggerLevel", "debug"),
					// The password version did not change, so the password is not sent again.
					expectCalls(server, "/api/config setpassword", 1),
				),
			},
			{
				// A new password, with the old password for the connection.
				Config: accProvider(server, "n3w-pw") + `
provider "matterbridge" {
  alias        = "unused"
  url          = "http://127.0.0.1:1"
}
resource "matterbridge_settings" "this" {
  password_wo         = "n3w-pw"
  password_wo_version = "2"
}`,
				ExpectError: regexp.MustCompile(`rejected the password`),
			},
			{
				Config: strings.Replace(accProvider(server, "n3w-pw"), "timeout ", "old_password = \"s3cret-pw\"\n  timeout ", 1) + `
resource "matterbridge_settings" "this" {
  password_wo         = "n3w-pw"
  password_wo_version = "2"
}`,
				Check: serverCheck(func() error {
					if server.Password() != "n3w-pw" {
						return fmt.Errorf("password not changed")
					}
					return nil
				}),
			},
			{
				ResourceName:            "matterbridge_settings.this",
				ImportState:             true,
				ImportStateId:           "matterbridge",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"password_wo", "password_wo_version"},
			},
		},
	})
}

// z2mConfig is all of the configJson of the fake zigbee2mqtt plugin, without name, type, version and password.
const z2mConfig = `
  config = {
    host                   = "mqtt://localhost"
    port                   = 1883
    protocolVersion        = 5
    username               = "matterbridge"
    rejectUnauthorized     = true
    topic                  = "zigbee2mqtt"
    zigbeeFrontend         = "http://localhost:8080"
    whiteList              = %s
    blackList              = []
    switchList             = []
    lightList              = []
    outletList             = []
    featureBlackList       = []
    scenesType             = "outlet"
    scenesPrefix           = true
    postfix                = ""
    debug                  = false
    unregisterOnShutdown   = false
    deviceFeatureBlackList = %s
  }
  sensitive_config = { password = "fake-mqtt-password" }
`

func TestAccPlugin(t *testing.T) {
	server := accServer(t, fakematterbridge.Options{})
	// The import step does not get the provider address from the environment.
	z2m := func(body string) string {
		return `
terraform {
  required_providers {
    matterbridge = { source = "jellyfrog/matterbridge" }
  }
}
` + accProvider(server, "") + `
resource "matterbridge_plugin" "z2m" {
  name = "matterbridge-zigbee2mqtt"
` + body + `
}`
	}
	step := &stepCalls{server: server}
	whiteList := `["Office Ceiling", "Hallway Motion Sensor"]`
	features := `{ "Hallway Motion Sensor" = ["illuminance", "temperature"] }`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: accFactories,
		Steps: []resource.TestStep{
			{
				// Import, then the same config: the plan is empty.
				ResourceName:       "matterbridge_plugin.z2m",
				ImportState:        true,
				ImportStateId:      fakematterbridge.Zigbee2MQTT,
				ImportStatePersist: true,
				Config:             z2m(`version = "3.3.0"` + fmt.Sprintf(z2mConfig, `[]`, `{}`)),
			},
			{
				Config:   z2m(`version = "3.3.0"` + fmt.Sprintf(z2mConfig, `[]`, `{}`)),
				PlanOnly: true,
			},
			{
				Config:      z2m(`config = { password = "x" }`),
				ExpectError: regexp.MustCompile(`Secret in config`),
			},
			{
				Config:      z2m(`config = { version = "1" }`),
				ExpectError: regexp.MustCompile(`cannot set version`),
			},
			{
				// The schema error shows the path, not the value.
				Config:      z2m(`sensitive_config = { topic = "not-secret" }` + "\n" + `config = { port = "not-a-port", scenesPrefix = "yes" }`),
				ExpectError: regexp.MustCompile(`(?s)config\.port: must be number.*config\.scenesPrefix: must be boolean`),
			},
			{
				Config: z2m(`version = "3.3.0"` + fmt.Sprintf(z2mConfig, whiteList, features)),
				Check: resource.ComposeTestCheckFunc(
					expectPluginConfig(server, fakematterbridge.Zigbee2MQTT, "whiteList", []any{"Office Ceiling", "Hallway Motion Sensor"}),
					expectPluginConfig(server, fakematterbridge.Zigbee2MQTT, "deviceFeatureBlackList", map[string]any{"Hallway Motion Sensor": []any{"illuminance", "temperature"}}),
					expectPluginConfig(server, fakematterbridge.Zigbee2MQTT, "password", "fake-mqtt-password"),
					expectPluginConfig(server, fakematterbridge.Zigbee2MQTT, "version", "3.3.0"),
					expectCalls(server, "/api/savepluginconfig", 1),
					// restart = "auto" in bridge mode: Matterbridge, because a plugin restart loses the devices.
					expectCalls(server, "/api/restartplugin", 0),
					expectCalls(server, "/api/restart", 1),
					resource.TestCheckResourceAttr("matterbridge_plugin.z2m", "restart", "auto"),
					resource.TestCheckResourceAttr("matterbridge_plugin.z2m", "registered_devices", "29"),
				),
			},
			{
				// Only some keys: the others stop being managed and do not change. Nothing is sent.
				Config: z2m(`config = { whiteList = ` + whiteList + ` }`),
				Check: resource.ComposeTestCheckFunc(
					expectCalls(server, "/api/savepluginconfig", 1),
					expectPluginConfig(server, fakematterbridge.Zigbee2MQTT, "topic", "zigbee2mqtt"),
					resource.TestCheckResourceAttr("matterbridge_plugin.z2m", "version", "3.3.0"),
				),
			},
			{
				// A key that comes back with its current value, and a key that moves to sensitive_config: the plan
				// changes the state, but the apply sends nothing.
				PreConfig: step.reset,
				Config: z2m(`config = { whiteList = ` + whiteList + `, topic = "zigbee2mqtt" }` + "\n" +
					`sensitive_config = { username = "matterbridge" }`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("matterbridge_plugin.z2m", plancheck.ResourceActionUpdate)}},
				Check:            step.expect(map[string]int{}),
			},
			{
				// A person changes the white list in the frontend. The plan changes it back.
				PreConfig: func() {
					p := server.Plugin(fakematterbridge.Zigbee2MQTT)
					p.ConfigJSON["whiteList"] = []any{"Coordinator"}
					mustDo(t, server, "/api/savepluginconfig", map[string]any{"pluginName": p.Name, "formData": p.ConfigJSON})
				},
				Config:           z2m(`config = { whiteList = ` + whiteList + ` }`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("matterbridge_plugin.z2m", plancheck.ResourceActionUpdate)}},
				Check: resource.ComposeTestCheckFunc(
					expectPluginConfig(server, fakematterbridge.Zigbee2MQTT, "whiteList", []any{"Office Ceiling", "Hallway Motion Sensor"}),
					expectCalls(server, "/api/restart", 2),
				),
			},
			{
				// restart = "plugin" in bridge mode: the plugin restart loses the devices (as in Matterbridge 3.11),
				// so the provider restarts Matterbridge.
				Config: z2m(`restart = "plugin"` + "\n" + `config = { whiteList = ["Office Ceiling"] }`),
				Check: resource.ComposeTestCheckFunc(
					expectCalls(server, "/api/restartplugin", 1),
					expectCalls(server, "/api/restart", 3),
					resource.TestCheckResourceAttr("matterbridge_plugin.z2m", "registered_devices", "29"),
				),
			},
			{
				// In childbridge mode, restart = "auto" restarts only the plugin, and the devices stay.
				PreConfig: func() { server.SetBridgeMode("childbridge") },
				Config:    z2m(`config = { whiteList = ` + whiteList + ` }`),
				Check: resource.ComposeTestCheckFunc(
					expectCalls(server, "/api/restartplugin", 2),
					expectCalls(server, "/api/restart", 3),
					resource.TestCheckResourceAttr("matterbridge_plugin.z2m", "registered_devices", "29"),
					serverCheck(func() error { server.SetBridgeMode("bridge"); return nil }),
				),
			},
			{
				// A downgrade installs the version and restarts Matterbridge.
				Config: z2m(`version = "3.2.0"` + "\n" + `config = { whiteList = ` + whiteList + ` }`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matterbridge_plugin.z2m", "version", "3.2.0"),
					expectCalls(server, "/api/install", 1),
					expectCalls(server, "/api/restart", 4),
					serverCheck(func() error {
						if v := server.Plugin(fakematterbridge.Zigbee2MQTT).Version; v != "3.2.0" {
							return fmt.Errorf("version %s", v)
						}
						return nil
					}),
				),
			},
			{
				Config:      z2m(`version = "9.9.9"`),
				ExpectError: regexp.MustCompile(`npm install matterbridge-zigbee2mqtt@9.9.9 failed`),
			},
			{
				Config: z2m(`version = "3.2.0"` + "\n" + `enabled = false`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matterbridge_plugin.z2m", "enabled", "false"),
					expectCalls(server, "/api/disableplugin", 1),
				),
			},
			{
				// A config change while disabled: save only. The plugin is not running, so nothing restarts.
				PreConfig: step.reset,
				Config:    z2m(`enabled = false` + "\n" + `config = { whiteList = ["Office Ceiling"] }`),
				Check:     step.expect(map[string]int{"/api/savepluginconfig": 1}),
			},
			{
				// Enable with a config change: enable loads the new config, so there is no restart.
				PreConfig: step.reset,
				Config:    z2m(`config = { whiteList = ` + whiteList + ` }`),
				Check: resource.ComposeTestCheckFunc(
					step.expect(map[string]int{"/api/savepluginconfig": 1, "/api/enableplugin": 1}),
					resource.TestCheckResourceAttr("matterbridge_plugin.z2m", "enabled", "true"),
					resource.TestCheckResourceAttr("matterbridge_plugin.z2m", "registered_devices", "29"),
				),
			},
		},
	})
	// Destroy did not change the plugin.
	if p := server.Plugin(fakematterbridge.Zigbee2MQTT); p == nil || server.Installed(fakematterbridge.Zigbee2MQTT) == "" {
		t.Fatal("destroy removed the plugin")
	}
}

// TestAccHassImport imports the hass plugin, whose token goes to sensitive_config.
func TestAccHassImport(t *testing.T) {
	server := accServer(t, fakematterbridge.Options{})
	config := `
terraform {
  required_providers {
    matterbridge = { source = "jellyfrog/matterbridge" }
  }
}
` + accProvider(server, "") + `
resource "matterbridge_plugin" "hass" {
  name = "matterbridge-hass"
  config = {
    host = "ws://homeassistant.local:8123", certificatePath = "", rejectUnauthorized = true, reconnectTimeout = 60, reconnectRetries = 10
    filterByArea = "", filterByLabel = "", whiteList = [], blackList = [], entityWhiteList = [], entityBlackList = [], splitEntities = []
    splitByLabel = "", splitNameStrategy = "Entity name", controllerStrategy = "Merge", namePostfix = "", postfix = "", airQualityRegex = ""
    enableServerRvc = false, discardHiddenEntities = false, virtualControlLabel = "", debug = false, unregisterOnShutdown = false
    applyFiltersToDeviceEntities = false, serialPostfix = "", deviceEntityBlackList = {}
  }
  sensitive_config = { token = "fake-ha-token" }
}`
	labelOnly := strings.Replace(config, `filterByLabel = ""`, `filterByLabel = "matter"`, 1)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: accFactories,
		Steps: []resource.TestStep{
			{
				Config:             config,
				ResourceName:       "matterbridge_plugin.hass",
				ImportState:        true,
				ImportStateId:      fakematterbridge.Hass,
				ImportStatePersist: true,
			},
			{Config: config, PlanOnly: true},
			{
				Config: labelOnly,
				Check: resource.ComposeTestCheckFunc(
					expectPluginConfig(server, fakematterbridge.Hass, "filterByLabel", "matter"),
					expectPluginConfig(server, fakematterbridge.Hass, "token", "fake-ha-token"),
					expectCalls(server, "/api/restartplugin", 0),
					expectCalls(server, "/api/restart", 1),
				),
			},
		},
	})
}

func TestAccPluginInstall(t *testing.T) {
	server := accServer(t, fakematterbridge.Options{})
	config := accProvider(server, "") + `
resource "matterbridge_plugin" "shelly" {
  name              = "matterbridge-shelly"
  version           = "2.0.0"
  config            = { username = "admin", debug = true }
  sensitive_config  = { password = "shelly-pw" }
  remove_on_destroy = true
}`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: accFactories,
		CheckDestroy: func(*terraform.State) error {
			if server.Plugin(fakematterbridge.Shelly) != nil || server.Installed(fakematterbridge.Shelly) != "" {
				return fmt.Errorf("the plugin is still there")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matterbridge_plugin.shelly", "version", "2.0.0"),
					resource.TestCheckResourceAttr("matterbridge_plugin.shelly", "enabled", "true"),
					resource.TestCheckResourceAttr("matterbridge_plugin.shelly", "latest_version", "2.1.0"),
					expectPluginConfig(server, fakematterbridge.Shelly, "password", "shelly-pw"),
					expectPluginConfig(server, fakematterbridge.Shelly, "username", "admin"),
					expectCalls(server, "/api/addplugin", 1),
					expectCalls(server, "/api/restart", 1),
					expectCalls(server, "/api/restartplugin", 0),
				),
			},
			{Config: config, PlanOnly: true},
		},
	})
}

func TestAccDataSources(t *testing.T) {
	server := accServer(t, fakematterbridge.Options{})
	p := server.Plugin(fakematterbridge.Zigbee2MQTT)
	p.ConfigJSON["whiteList"] = []any{"Office Ceiling"}
	if err := server.Do("/api/savepluginconfig", map[string]any{"pluginName": p.Name, "formData": p.ConfigJSON}); err != nil {
		t.Fatal(err)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: accFactories,
		Steps: []resource.TestStep{{
			Config: accProvider(server, "") + `
data "matterbridge_plugins" "all" {}
data "matterbridge_devices" "hass" { plugin = "matterbridge-hass" }
data "matterbridge_devices" "all" {}
data "matterbridge_select_devices" "z2m" { plugin = "matterbridge-zigbee2mqtt" }
data "matterbridge_matter" "bridge" {}

output "selected" {
  value = [for d in data.matterbridge_select_devices.z2m.devices : d.name if d.selected]
}
output "has_kitchen" {
  value = contains(data.matterbridge_select_devices.z2m.names, "Office Ceiling")
}
`,
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("data.matterbridge_plugins.all", "plugins.#", "2"),
				resource.TestCheckResourceAttr("data.matterbridge_plugins.all", "plugins.0.name", "matterbridge-zigbee2mqtt"),
				resource.TestCheckResourceAttr("data.matterbridge_plugins.all", "plugins.0.registered_devices", "29"),
				resource.TestCheckResourceAttr("data.matterbridge_plugins.all", "plugins.1.version", "1.6.0"),
				resource.TestCheckResourceAttr("data.matterbridge_devices.hass", "devices.#", "1"),
				resource.TestCheckResourceAttr("data.matterbridge_devices.hass", "devices.0.name", "Café Fridge Plug"),
				resource.TestCheckResourceAttr("data.matterbridge_devices.all", "devices.#", "6"),
				resource.TestCheckResourceAttr("data.matterbridge_devices.all", "devices.1.type", "DimmableLight (0x0101)"),
				resource.TestCheckResourceAttr("data.matterbridge_select_devices.z2m", "devices.#", "5"),
				resource.TestCheckOutput("has_kitchen", "true"),
				resource.TestCheckResourceAttr("data.matterbridge_select_devices.z2m", "devices.3.name", "Office Ceiling"),
				resource.TestCheckResourceAttr("data.matterbridge_select_devices.z2m", "devices.3.selected", "true"),
				resource.TestCheckResourceAttr("data.matterbridge_select_devices.z2m", "devices.3.registered", "true"),
				resource.TestCheckResourceAttr("data.matterbridge_select_devices.z2m", "devices.0.selected", "false"),
				resource.TestCheckResourceAttr("data.matterbridge_select_devices.z2m", "devices.3.entities.0.name", "state"),
				resource.TestCheckResourceAttr("data.matterbridge_matter.bridge", "node", "Matterbridge"),
				resource.TestCheckResourceAttr("data.matterbridge_matter.bridge", "commissioned", "true"),
				resource.TestCheckResourceAttr("data.matterbridge_matter.bridge", "fabrics.0.vendor_name", "(AppleHome)"),
				resource.TestCheckResourceAttr("data.matterbridge_matter.bridge", "fabrics.0.label", "My Home"),
				resource.TestCheckResourceAttrSet("data.matterbridge_matter.bridge", "qr_pairing_code"),
				expectCalls(server, "/api/restart", 0),
			),
		}},
	})
	if calls := server.Calls(); len(calls) != 1 { // the savepluginconfig of the test
		t.Fatalf("the data sources changed something: %v", calls)
	}
}

func TestAccCommissioning(t *testing.T) {
	server := accServer(t, fakematterbridge.Options{})
	config := func(action, trigger string) string {
		return accProvider(server, "") + fmt.Sprintf(`
resource "matterbridge_commissioning" "window" {
  action  = %q
  trigger = %q
}`, action, trigger)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: accFactories,
		Steps: []resource.TestStep{
			{
				Config: config("open", "2026-10-28"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matterbridge_commissioning.window", "window_status", "2"),
					expectCalls(server, "/api/matter startCommission", 1),
				),
			},
			{Config: config("open", "2026-10-28"), PlanOnly: true},
			{
				// A change to action alone does not send a command.
				Config: config("close", "2026-10-28"),
				Check:  expectCalls(server, "/api/matter stopCommission", 0),
			},
			{
				Config: config("close", "2026-10-29"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matterbridge_commissioning.window", "window_status", "0"),
					expectCalls(server, "/api/matter stopCommission", 1),
					expectCalls(server, "/api/matter startCommission", 1),
				),
			},
		},
	})
}
