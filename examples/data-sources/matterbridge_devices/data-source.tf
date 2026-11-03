data "matterbridge_devices" "zigbee2mqtt" {
  plugin = "matterbridge-zigbee2mqtt"
}

output "unreachable_devices" {
  value = [for d in data.matterbridge_devices.zigbee2mqtt.devices : d.name if !d.reachable]
}
