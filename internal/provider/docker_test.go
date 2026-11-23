package provider

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/jellyfrog/terraform-provider-matterbridge/internal/realmb"
)

// The tests TestAccDocker* use a real Matterbridge in Docker (internal/realmb). They run only with
// TF_ACC_DOCKER=1. The plugin test installs a plugin from npm, so it needs the internet.

// examplePlugin is a plugin of the author of Matterbridge. It needs no other system: it makes example devices.
const examplePlugin = "matterbridge-example-dynamic-platform"

// dockerProvider starts a Matterbridge for the test and returns a function that makes the provider block with a
// password.
func dockerProvider(t *testing.T) (func(password string) string, *realmb.Instance) {
	t.Helper()
	mb := realmb.Start(t)
	accEnv(t)
	return func(password string) string {
		return fmt.Sprintf(`
terraform {
  required_providers {
    matterbridge = { source = "jellyfrog/matterbridge" }
  }
}

provider "matterbridge" {
  url             = %q
  password        = %q
  timeout         = 60
  restart_timeout = 600
}
`, mb.URL, password)
	}, mb
}

// TestAccDockerSettings changes settings of a real Matterbridge: a log level (no restart), the bridge mode and
// the Matter port (one full restart), and the frontend password.
func TestAccDockerSettings(t *testing.T) {
	providerBlock, _ := dockerProvider(t)
	const settings = "matterbridge_settings.this"
	config := func(password, body string) string {
		return providerBlock(password) + `
resource "matterbridge_settings" "this" {
` + body + `
}`
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: accFactories,
		Steps: []resource.TestStep{
			{
				Config: config("", `mb_log_level = "debug"`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(settings, "mb_log_level", "debug"),
					resource.TestCheckResourceAttr(settings, "bridge_mode", "bridge"),
					resource.TestCheckResourceAttr(settings, "matter_port", "5540"),
					resource.TestCheckResourceAttr(settings, "matterbridge_version", realmb.DefaultImageTag),
					resource.TestCheckResourceAttr(settings, "restart_required", "false"),
				),
			},
			{Config: config("", `mb_log_level = "debug"`), PlanOnly: true},
			{
				// Matterbridge uses these settings only after a restart. The apply restarts it one time.
				Config: config("", `
  mb_log_level = "debug"
  bridge_mode  = "childbridge"
  matter_port  = 5541`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(settings, "bridge_mode", "childbridge"),
					resource.TestCheckResourceAttr(settings, "matter_port", "5541"),
					resource.TestCheckResourceAttr(settings, "restart_required", "false"),
				),
			},
			{
				Config: config("", `
  mb_log_level = "debug"
  bridge_mode  = "childbridge"
  matter_port  = 5541`),
				PlanOnly: true,
			},
			{
				// The provider password is the new password. Before the change, Matterbridge has no password, so the
				// provider connects with "". After it, Matterbridge accepts only the new password.
				Config: config("s3cret-pw", `
  mb_log_level        = "debug"
  bridge_mode         = "childbridge"
  matter_port         = 5541
  password_wo         = "s3cret-pw"
  password_wo_version = "1"`),
			},
			{
				Config: config("s3cret-pw", `
  mb_log_level        = "debug"
  bridge_mode         = "childbridge"
  matter_port         = 5541
  password_wo         = "s3cret-pw"
  password_wo_version = "1"`) + `
data "matterbridge_plugins" "all" {}`,
				Check: resource.TestCheckResourceAttrSet("data.matterbridge_plugins.all", "plugins.#"),
			},
		},
	})
}

// TestAccDockerPlugin installs a plugin from npm into a real Matterbridge, changes its config, imports it and
// removes it.
func TestAccDockerPlugin(t *testing.T) {
	providerBlock, mb := dockerProvider(t)
	const plugin = "matterbridge_plugin.example"
	config := func(debug bool) string {
		return providerBlock("") + fmt.Sprintf(`
resource "matterbridge_plugin" "example" {
  name              = %q
  version           = "3.1.0"
  config            = { debug = %t, whiteList = [] }
  remove_on_destroy = true
}`, examplePlugin, debug)
	}
	devices := func(s *terraform.State) error {
		r := s.RootModule().Resources[plugin]
		if r == nil {
			return fmt.Errorf("%s is not in the state", plugin)
		}
		if n, _ := strconv.Atoi(r.Primary.Attributes["registered_devices"]); n == 0 {
			return fmt.Errorf("the plugin has no devices: %v", r.Primary.Attributes)
		}
		return nil
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: accFactories,
		CheckDestroy: func(*terraform.State) error {
			c, err := NewClient(ClientConfig{URL: mb.URL, Passwords: []string{""}})
			if err != nil {
				return err
			}
			defer c.Close()
			p, err := c.Plugin(context.Background(), examplePlugin)
			if err != nil || p != nil {
				return fmt.Errorf("the plugin after the destroy: %+v %v", p, err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				// npm installs the plugin, Matterbridge adds it and restarts.
				Config: config(false),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(plugin, "version", "3.1.0"),
					resource.TestCheckResourceAttr(plugin, "enabled", "true"),
					// latest_version can be empty: a worker of Matterbridge checks npm some time after the start.
					devices,
				),
			},
			{Config: config(false), PlanOnly: true},
			{
				// A config change: the apply saves the config and restarts (bridge mode: all of Matterbridge).
				Config: config(true),
				Check:  resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr(plugin, "config.debug", "true"), devices),
			},
			{Config: config(true), PlanOnly: true},
			{
				Config: config(true) + fmt.Sprintf(`
data "matterbridge_plugins" "all" {}
data "matterbridge_devices" "example" { plugin = %q }`, examplePlugin),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.matterbridge_devices.example", "devices.0.name"),
				),
			},
			{
				Config:        config(true),
				ResourceName:  plugin,
				ImportState:   true,
				ImportStateId: examplePlugin,
			},
		},
	})
}
