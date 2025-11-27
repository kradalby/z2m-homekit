package events

import (
	"time"
)

// StateUpdateEvent carries device state for SSE subscribers and HAP updates.
type StateUpdateEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Source    string    `json:"source"`
	DeviceID  string    `json:"device_id"`
	Name      string    `json:"name"`

	// Sensor values (pointers to distinguish unset from zero)
	Temperature *float64 `json:"temperature,omitempty"`
	Humidity    *float64 `json:"humidity,omitempty"`
	Battery     *int     `json:"battery,omitempty"`
	Occupancy   *bool    `json:"occupancy,omitempty"`
	Illuminance *int     `json:"illuminance,omitempty"`
	Pressure    *float64 `json:"pressure,omitempty"`
	Contact     *bool    `json:"contact,omitempty"`     // true = closed, false = open
	WaterLeak   *bool    `json:"water_leak,omitempty"`  // true = leak detected
	Smoke       *bool    `json:"smoke,omitempty"`       // true = smoke detected
	Tamper      *bool    `json:"tamper,omitempty"`      // true = tampered

	// Light values
	On         *bool    `json:"on,omitempty"`
	Brightness *int     `json:"brightness,omitempty"` // 0-100 (HAP scale)
	Hue        *float64 `json:"hue,omitempty"`        // 0-360
	Saturation *float64 `json:"saturation,omitempty"` // 0-100
	ColorTemp  *int     `json:"color_temp,omitempty"` // mireds

	// Fan values
	FanSpeed *int `json:"fan_speed,omitempty"` // 0-100 (percentage)

	// Connectivity
	LinkQuality     int       `json:"link_quality"`
	LastSeen        time.Time `json:"last_seen"`
	LastUpdated     time.Time `json:"last_updated"`
	ConnectionState string    `json:"connection_state"`
	ConnectionNote  string    `json:"connection_note"`
}

// CommandType represents supported device commands.
type CommandType string

const (
	CommandTypeSetPower      CommandType = "set_power"
	CommandTypeSetBrightness CommandType = "set_brightness"
	CommandTypeSetColor      CommandType = "set_color"
	CommandTypeSetColorTemp  CommandType = "set_color_temp"
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
	Hue        *float64 `json:"hue,omitempty"`
	Saturation *float64 `json:"saturation,omitempty"`
	ColorTemp  *int     `json:"color_temp,omitempty"`
}

// Equals determines whether two events carry the same logical state (ignoring timestamp/source).
func (e StateUpdateEvent) Equals(other StateUpdateEvent) bool {
	return e.DeviceID == other.DeviceID &&
		e.Name == other.Name &&
		ptrBoolEqual(e.On, other.On) &&
		ptrIntEqual(e.Brightness, other.Brightness) &&
		ptrFloatEqual(e.Hue, other.Hue) &&
		ptrFloatEqual(e.Saturation, other.Saturation) &&
		ptrIntEqual(e.ColorTemp, other.ColorTemp) &&
		ptrFloatEqual(e.Temperature, other.Temperature) &&
		ptrFloatEqual(e.Humidity, other.Humidity) &&
		ptrIntEqual(e.Battery, other.Battery) &&
		ptrBoolEqual(e.Occupancy, other.Occupancy) &&
		ptrIntEqual(e.Illuminance, other.Illuminance) &&
		ptrFloatEqual(e.Pressure, other.Pressure) &&
		ptrBoolEqual(e.Contact, other.Contact) &&
		ptrBoolEqual(e.WaterLeak, other.WaterLeak) &&
		ptrBoolEqual(e.Smoke, other.Smoke) &&
		ptrBoolEqual(e.Tamper, other.Tamper) &&
		ptrIntEqual(e.FanSpeed, other.FanSpeed) &&
		e.LinkQuality == other.LinkQuality &&
		e.LastSeen.Equal(other.LastSeen) &&
		e.LastUpdated.Equal(other.LastUpdated) &&
		e.ConnectionState == other.ConnectionState &&
		e.ConnectionNote == other.ConnectionNote
}

func ptrBoolEqual(a, b *bool) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func ptrIntEqual(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func ptrFloatEqual(a, b *float64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	const eps = 0.001
	diff := *a - *b
	if diff < 0 {
		diff = -diff
	}
	return diff < eps
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
