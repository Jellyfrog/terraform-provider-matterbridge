# terraform-provider-matterbridge

This is an OpenTofu provider for [Matterbridge](https://github.com/Luligu/matterbridge). It supports only the latest OpenTofu version (1.13.2, set in `mise.toml`). The registry address is `registry.opentofu.org/jellyfrog/matterbridge`. The provider was made for Matterbridge 3.11.0 with matterbridge-zigbee2mqtt 3.3.0 and matterbridge-hass 1.6.0.

The provider uses only the WebSocket API of the frontend, as the frontend does (`packages/core/src/frontend.ts`). Each message is `{id, src, dst: "Matterbridge", method, params}`. The WebSocket upgrade checks `?password=`, so the connection is the authentication. The provider does not use the REST API: `/api/login` allows only the IP address, and Matterbridge removes it when no WebSocket client is connected.

Some changes restart Matterbridge or a plugin. The resources apply one at a time, and each resource restarts separately: if two plugins change `version` in one apply, Matterbridge restarts two times. During a restart of Matterbridge, HomeKit and other Matter controllers lose the devices for some time. A plan that will restart something shows a warning.

## Provider

```hcl
provider "matterbridge" {
  url      = "http://192.168.1.10:8283" # MATTERBRIDGE_URL; http, https, ws, wss
  password = var.matterbridge_password  # MATTERBRIDGE_PASSWORD
  # old_password (MATTERBRIDGE_OLD_PASSWORD): tried if Matterbridge does not accept password
  # timeout (seconds for each answer, default 30), restart_timeout (npm and restarts, default 300)
}
```

The provider tries `password`, then `old_password`, then `""`. Matterbridge accepts only the exact password, also if no password is set. Thus, `password` can be the password that `matterbridge_settings` sets: before the change the provider connects with `""`, and after the change with `password`.

The provider connects only when it needs data. Thus, `tofu validate` does not need Matterbridge.

## Resources

The `examples` directory has an example for each resource and data source. The `docs` directory has the reference for the OpenTofu Registry (`go generate ./...` makes it from the schema and the examples).

### `matterbridge_settings`

These are the settings of the settings page. There is one for each Matterbridge. The resource manages only the attributes that you set. The other attributes show the current values.

- `bridge_mode` (`bridge` or `childbridge`), `matter_port` (5540–5600), `matter_passcode`, `matter_discriminator` (0–4095), `mdns_interface`, `ipv4_address`, `ipv6_address` (`""` means all), `virtual_mode` (`disabled`, `light`, `outlet`, `switch`, `mounted_switch`): Matterbridge uses a new value only after a restart. Thus, if one of these changes, the apply restarts Matterbridge one time and waits until it and its plugins are back.
- `mb_log_level`, `mj_log_level` (`debug`, `info`, `notice`, `warn`, `error`, `fatal`), `mb_log_file`, `mj_log_file`: no restart.
- `password_wo` and `password_wo_version`: the frontend password. `password_wo` is write-only, so it is not in the plan or the state. The provider cannot read the password. It sets `password_wo` when `password_wo_version` changes. After the change, the provider uses the new password.
- Computed: `matterbridge_version`, `restart_required`. The Home Assistant add-on (or the Docker image) controls the version of Matterbridge, so the provider does not install Matterbridge.

The provider sends numbers as strings and capitalizes the log levels, as Matterbridge expects. Matterbridge does not answer some requests with an incorrect value. Thus, the provider validates the values in the plan.

Destroy does not change the settings. Import: `tofu import matterbridge_settings.this matterbridge`. After the import, the plan is empty if the values are the same. The exception is `password_wo_version`: the first apply sets the password one time.

### `matterbridge_plugin`

- `name`: the npm package. If you change it, OpenTofu replaces the resource.
- `version`: if it is different from the installed version, the apply runs `npm install <name>@<version>` (also for an older version), and restarts Matterbridge. Matterbridge shows a new version only after a restart. If you do not set `version`, the attribute shows the installed version.
- If Matterbridge does not have the plugin, the apply installs it, adds it (`/api/addplugin`), saves the config and restarts Matterbridge.
- `enabled` (default `true`).
- `config`: the plugin config (`configJson`) as an object. The provider manages only the top-level keys that you write. It compares each value as a whole, also lists and objects such as `whiteList` and `deviceFeatureBlackList`. To save, the provider reads the current config, sets the managed keys and sends the full config (`/api/savepluginconfig`). Other keys do not change. If you remove a key, the provider stops managing it, and Matterbridge keeps the value. You cannot set `name`, `type` and `version`. Matterbridge sets them.
- `sensitive_config`: more keys with string values, but OpenTofu does not show them. Put the secrets here, for example `{ password = var.mqtt_password }` for zigbee2mqtt and `{ token = var.ha_token }` for hass. The plan fails if `config` contains `password`, `token`, `key` or a setting that the plugin schema shows as a password. `sensitive_config` is a map of strings and not dynamic, because OpenTofu writes dynamic values of the state to its debug log.
- `restart`: what the apply restarts after a config change: `auto` (the default: `matterbridge` in bridge mode, `plugin` in childbridge mode), `plugin` (`/api/restartplugin`: only this plugin and its devices restart), `matterbridge`, or `none`.
  In bridge mode, Matterbridge 3.11 does not register the devices again after `/api/restartplugin`: the old endpoints stay in the aggregator, so the new ones cannot be added, and HomeKit loses them. Thus, `auto` restarts Matterbridge in bridge mode. If you set `plugin` in bridge mode, the plan shows a warning, and if the plugin has no devices after the plugin restart, the apply restarts Matterbridge.
- `remove_on_destroy` (default `false`): if true, destroy removes the plugin from Matterbridge and uninstalls the npm package. Otherwise, destroy does not change the plugin.
- Computed: `latest_version`, `registered_devices`.

During the plan, the provider validates the config with the `schemaJson` of the plugin (type, enum, required, items, minimum, maximum, length). The error shows the path and the rule, never the value. A problem in a key that the resource does not manage is only a warning. The plan also shows a warning for names in `whiteList`, `blackList` and `deviceFeatureBlackList`, and features in `deviceFeatureBlackList`, that the plugin does not offer (`/api/select/devices`). A device that is offline can also be missing, so this is not an error.

The plugin resource owns the device lists. There is no resource for each device: OpenTofu applies resources in parallel, and many resources that change one list would lose changes. Build the lists with `for` expressions, and check them with `matterbridge_select_devices`.

Import: `tofu import matterbridge_plugin.z2m matterbridge-zigbee2mqtt`. The state gets all of `configJson` (without `name`, `type` and `version`). `password`, `token`, `key` and the password settings of the schema go to `sensitive_config`. Thus, the plan is empty if the configuration has the same keys and values (for example from `tofu plan -generate-config-out`). If the configuration has fewer keys, the plan shows the other keys as removed one time, with a warning. The apply does not send them, and Matterbridge keeps them.

### `matterbridge_commissioning`

```hcl
resource "matterbridge_commissioning" "window" {
  action  = "open"        # open, close or advertise
  trigger = "2026-10-28"  # change it to send the command again
  # node = "Matterbridge" (default), or a plugin name in childbridge mode
}
```

The apply sends the command (`/api/matter` with `startCommission`, `stopCommission` or `advertise`) when it creates the resource and when `trigger` changes. A change to `action` alone, a plan or a refresh never sends a command. Destroy does nothing. The resource cannot remove a fabric.

## Data sources

- `matterbridge_plugins`: `plugins`, a list of `name`, `version`, `latest_version`, `type`, `description`, `enabled`, `loaded`, `started`, `configured`, `error`, `restart_required`, `registered_devices`. The config is not here, because it contains secrets.
- `matterbridge_devices` (`plugin` optional): the registered Matter devices: `plugin`, `name`, `serial_number`, `unique_id`, `id`, `endpoint`, `type`, `reachable`, `power_source`, `config_url`.
- `matterbridge_select_devices` (`plugin`): the devices that the plugin offers for its lists: `names`, and `devices` with `name`, `serial_number`, `entities { name, description }` (the features for `deviceFeatureBlackList`), `selected` (the current `whiteList` and `blackList` expose it) and `registered`.
- `matterbridge_matter` (`node`, default `Matterbridge`): `online`, `commissioned`, `advertising`, `window_status`, `serial_number`, `session_count`, `fabrics { index, fabric_id, node_id, vendor_id, vendor_name, label }`, and the sensitive `qr_pairing_code` and `manual_pairing_code`.

## Build and test

```sh
mise install            # OpenTofu 1.13.2
go build .
go generate ./...     # docs/ with tfplugindocs (uses tofu, no Terraform download)
go test ./...           # unit tests with a fake Matterbridge in the same process

# Acceptance tests: OpenTofu from mise with the fake Matterbridge
TF_ACC=1 go test ./internal/provider -run TestAcc -v
```

`internal/fakematterbridge` serves the WebSocket API as Matterbridge 3.11.0 does. Its plugins have the configJson and schemaJson of matterbridge-zigbee2mqtt 3.3.0 and matterbridge-hass 1.6.0. Its devices are made up. It has the same quirks as Matterbridge:

- The WebSocket password must be exactly the frontend password, also if it is empty.
- Some `/api/config` names do not answer an incorrect value. `setmatterport` and the other numbers need strings.
- `/api/savepluginconfig` answers before the config changes.
- `/api/install` answers at once. A snackbar broadcast tells when npm is done.
- `/api/plugins` shows a new version only after `/api/restart`. `setbridgemode` applies only after a restart.
- `/api/restart` drops the connections without an answer, and the server is down for a short time.
- In bridge mode, `/api/restartplugin` leaves the plugin with no registered devices until `/api/restart`.
- It sends log broadcasts between the answers.

Tests use `server.Do(...)` to make changes that the provider does not know about, as the frontend does.

To try the provider manually, run `go run ./cmd/fake-matterbridge`. The command shows its `url`. Then, in a CLI config, set `dev_overrides` for `registry.opentofu.org/jellyfrog/matterbridge` to this directory.
