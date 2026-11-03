terraform {
  required_providers {
    matterbridge = {
      source = "jellyfrog/matterbridge"
    }
  }
}

provider "matterbridge" {
  url      = "http://192.168.1.10:8283" # or MATTERBRIDGE_URL
  password = var.matterbridge_password  # or MATTERBRIDGE_PASSWORD; leave out if the frontend has no password
}

variable "matterbridge_password" {
  type      = string
  sensitive = true
}
