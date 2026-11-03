# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

OpenTofu provider for Matterbridge (`registry.opentofu.org/jellyfrog/matterbridge`), built with terraform-plugin-framework. It targets Matterbridge 3.11.0 (API source: `packages/core/src/frontend.ts` in github.com/Luligu/matterbridge) README.md is the user documentation; keep it in sync with schema changes.

## Commands

```sh
mise install                       # OpenTofu 1.13.2 (only the latest OpenTofu is supported)
go build .                         # provider binary
go test ./...                      # unit tests (client against the in-process fake)
TF_ACC=1 go test ./internal/provider -run TestAcc -v           # acceptance tests with real tofu + fake server
TF_ACC=1 go test ./internal/provider -run TestAccPlugin -count=1 -v   # one test
go run ./cmd/fake-matterbridge     # fake server for manual runs (prints url); use dev_overrides
gofmt -l . && go vet ./...
```

Acceptance tests find tofu with `mise which tofu` (override with `TF_ACC_TERRAFORM_PATH`). An import step that is the first step (or after only `ExpectError` steps) needs an explicit `terraform { required_providers { matterbridge = { source = "jellyfrog/matterbridge" } } }` block in its config.

## Architecture

- `internal/provider/client.go`: WebSocket-only client. The `?password=` on the WS upgrade is the authentication. REST `/api/login` is avoided: it allows by IP, and Matterbridge clears that when no WS client is connected. Requests are `{id, src, dst:"Matterbridge", method, params}`, answers match by id, and broadcasts (id 0) go to subscribers. Passwords are tried in order: `password`, `old_password`, then `""`. Matterbridge requires an exact match even when no password is set. `gate` (RWMutex) blocks all calls during `Restart`. `Lock()` serializes resource mutations, because one resource may restart Matterbridge.
- Matterbridge API quirks that the client and resources depend on:
  - Some `/api/config` names never answer an invalid value, so validate at plan time and rely on the per-request timeout.
  - Numbers (`setmatterport`, passcode, discriminator) must be sent as strings. Log levels are sent capitalized but read back lowercase.
  - `/api/savepluginconfig` is fire-and-forget, so poll `/api/plugins` until the config matches. The config must contain the correct `name` and `type`, or Matterbridge silently ignores it (and logs it, secrets included).
  - `/api/install` completes asynchronously. Success or failure arrives only as a `snackbar` broadcast ("Installed package X" / "Package X not installed"). A new plugin version shows only after a full `/api/restart`, and so does `setbridgemode`.
  - In bridge mode, `/api/restartplugin` loses the plugin's devices (the old endpoints stay in the aggregator, and re-adding them fails). That is why `restart = "auto"` means a full restart in bridge mode, and `plugin` falls back to a full restart if `registeredDevices` stays 0.
  - `/api/restart` drops the socket without answering. Wait for a new `startupAt` and for the plugins to be started.
- `internal/fakematterbridge`: fake WS server that reproduces those quirks. Its `testdata/` has the configJson and schemaJson of matterbridge-zigbee2mqtt 3.3.0 and matterbridge-hass 1.6.0, made-up devices, and fake secrets (`fake-mqtt-password`, `fake-ha-token`). Tests use `server.Do(...)` to make out-of-band frontend changes and `server.Calls()` to assert which mutating methods ran.
- `plugin_resource.go`: `config` (dynamic) and `sensitive_config` (`map(string)`, not dynamic, because OpenTofu core logs dynamic state values at DEBUG) manage only their top-level keys.
  - Writes are read-modify-write of the full configJson. `name`, `type` and `version` are never managed.
  - Removing a key stops managing it; the value stays in Matterbridge.
  - Import puts all of configJson into state, with secret keys (`fixedSecretKeys` plus schema `ui:widget: password`) in `sensitive_config`, so that a matching config gives an empty plan.
  - `ModifyPlan` validates against the plugin's `schemaJson` (`schema_validate.go`). Messages must show paths and rules, never values. It also warns about unknown devices or features from `/api/select/devices` and about planned restarts.
  - There is deliberately no per-device resource: the plugin resource owns `whiteList` and `deviceFeatureBlackList`.
- `settings_resource.go`: table-driven (`settingSpecs`) mapping of attribute ↔ `matterbridgeInformation` key ↔ `/api/config` name. All attributes are Optional+Computed with UseStateForUnknown, so only the values you set are managed. Restart-requiring changes trigger one full restart per apply. `password_wo` is write-only and is set when `password_wo_version` changes.
- `json.go`: conversions between dynamic values and JSON. `dynamicFromJSON` keeps the prior HCL types when the content is equal, which avoids type-only diffs.

## Safety

A restart of a real Matterbridge makes the Matter controllers (Apple Home and others) lose its devices for some time. Against a real instance, run only reads (plan, data sources) unless asked to apply. Plugin configJson contains secrets (MQTT passwords, Home Assistant tokens), so never print it unredacted.
