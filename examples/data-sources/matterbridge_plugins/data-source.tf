data "matterbridge_plugins" "all" {}

output "plugin_versions" {
  value = { for p in data.matterbridge_plugins.all.plugins : p.name => p.version }
}
