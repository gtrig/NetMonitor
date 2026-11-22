package model

// Event types.
const (
	EventDeviceNew        = "device_new"
	EventDeviceOffline    = "device_offline"
	EventDeviceBackOnline = "device_back_online"
	EventIPChanged        = "ip_changed"
	EventHostnameChanged  = "hostname_changed"
	EventMACChangedForIP  = "mac_changed_for_ip"
	EventBaseline         = "baseline"
)

// EventTypes lists every valid event type.
var EventTypes = []string{
	EventDeviceNew, EventDeviceOffline, EventDeviceBackOnline,
	EventIPChanged, EventHostnameChanged, EventMACChangedForIP, EventBaseline,
}
