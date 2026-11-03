locals {
  # The devices to expose to Matter, with the features to hide for each device.
  matter_devices = {
    "Office Ceiling"        = []
    "Hallway Motion Sensor" = ["illuminance", "device_temperature"]
  }
}

resource "matterbridge_plugin" "zigbee2mqtt" {
  name    = "matterbridge-zigbee2mqtt"
  version = "3.3.0"
  config = {
    host                   = "mqtt://localhost"
    port                   = 1883
    username               = "matterbridge"
    topic                  = "zigbee2mqtt"
    whiteList              = keys(local.matter_devices)
    deviceFeatureBlackList = { for name, features in local.matter_devices : name => features if length(features) > 0 }
  }
  sensitive_config = { password = var.mqtt_password }
}

resource "matterbridge_plugin" "hass" {
  name             = "matterbridge-hass"
  version          = "1.6.0"
  config           = { host = "ws://homeassistant.local:8123", filterByLabel = "matter" }
  sensitive_config = { token = var.home_assistant_token }
}
