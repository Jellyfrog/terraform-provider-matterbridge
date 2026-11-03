resource "matterbridge_settings" "this" {
  bridge_mode  = "bridge"
  matter_port  = 5540
  virtual_mode = "outlet"
  mb_log_level = "info"

  # The frontend password. It is write-only: change password_wo_version to set it again.
  password_wo         = var.matterbridge_password
  password_wo_version = "1"
}
