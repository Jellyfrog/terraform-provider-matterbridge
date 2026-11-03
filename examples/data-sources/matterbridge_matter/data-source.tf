data "matterbridge_matter" "bridge" {}

output "paired_controllers" {
  value = [for f in data.matterbridge_matter.bridge.fabrics : "${f.vendor_name} ${f.label}"]
}
