# Opens the commissioning window, so that a new Matter controller can pair. Change trigger to open it again.
resource "matterbridge_commissioning" "window" {
  action  = "open"
  trigger = "2026-11-03"
}
