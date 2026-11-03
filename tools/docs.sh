#!/bin/sh
# Generates docs/ with tfplugindocs. It gets the provider schema from OpenTofu (TOFU, or tofu in PATH, or
# `mise which tofu`), so it does not download Terraform. Run it with `go generate ./...`.
set -eu
cd "$(dirname "$0")/.."
tofu=${TOFU:-$(command -v tofu || mise which tofu)}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
go build -o "$work/terraform-provider-matterbridge" .
cat > "$work/tofurc" <<RC
provider_installation {
  dev_overrides { "registry.opentofu.org/jellyfrog/matterbridge" = "$work" }
  direct {}
}
RC
mkdir "$work/config"
printf 'terraform {\n  required_providers {\n    matterbridge = { source = "jellyfrog/matterbridge" }\n  }\n}\n' > "$work/config/main.tf"
TF_CLI_CONFIG_FILE="$work/tofurc" "$tofu" -chdir="$work/config" providers schema -json > "$work/tofu.json"
# tfplugindocs finds the provider by its short name, not by the OpenTofu registry address.
jq '.provider_schemas |= with_entries(.key |= sub(".*/"; ""))' "$work/tofu.json" > "$work/schema.json"
go tool tfplugindocs generate --provider-name matterbridge --rendered-provider-name Matterbridge --providers-schema "$work/schema.json"
