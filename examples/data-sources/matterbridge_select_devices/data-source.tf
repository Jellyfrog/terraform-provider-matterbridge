data "matterbridge_select_devices" "zigbee2mqtt" {
  plugin = "matterbridge-zigbee2mqtt"
}

# Fail the plan if a device in the white list does not exist in the plugin.
check "devices_exist" {
  assert {
    condition     = alltrue([for name in ["Office Ceiling"] : contains(data.matterbridge_select_devices.zigbee2mqtt.names, name)])
    error_message = "A device in the white list is not offered by matterbridge-zigbee2mqtt."
  }
}
