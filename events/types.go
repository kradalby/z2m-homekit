package events

import (
	"time"
)

// CommandType represents supported device commands.
type CommandType string

const (
	CommandTypeSetPower      CommandType = "set_power"
	CommandTypeSetBrightness CommandType = "set_brightness"
	CommandTypeSetColor      CommandType = "set_color"
	CommandTypeSetColorTemp  CommandType = "set_color_temp"
	CommandTypeSetFanSpeed   CommandType = "set_fan_speed"
)

// CommandEvent captures requested control actions for a device.
type CommandEvent struct {
	Timestamp   time.Time   `json:"timestamp"`
	Source      string      `json:"source"`
	DeviceID    string      `json:"device_id"`
	CommandType CommandType `json:"command_type"`

	// Command payloads (only one set per event)
	On         *bool    `json:"on,omitempty"`
	Brightness *int     `json:"brightness,omitempty"` // 0-100 (HAP scale)
	FanSpeed   *int     `json:"fan_speed,omitempty"`  // 0-100 (percentage)
	Hue        *float64 `json:"hue,omitempty"`
	Saturation *float64 `json:"saturation,omitempty"`
	ColorTemp  *int     `json:"color_temp,omitempty"`
}

// ConnectionStatusEvent conveys component lifecycle information (web, HAP, MQTT, etc.).
type ConnectionStatusEvent struct {
	Timestamp  time.Time        `json:"timestamp"`
	Component  string           `json:"component"`
	Status     ConnectionStatus `json:"status"`
	Error      string           `json:"error"`
	Reconnects int              `json:"reconnects"`
}

// ConnectionStatus represents lifecycle state for a component.
type ConnectionStatus string

const (
	ConnectionStatusDisconnected ConnectionStatus = "disconnected"
	ConnectionStatusConnecting   ConnectionStatus = "connecting"
	ConnectionStatusConnected    ConnectionStatus = "connected"
	ConnectionStatusReconnecting ConnectionStatus = "reconnecting"
	ConnectionStatusFailed       ConnectionStatus = "failed"
)
