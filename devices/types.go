package devices

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/tailscale/hujson"
)

// DeviceType represents the type of Zigbee device.
type DeviceType string

const (
	DeviceTypeClimateSensor   DeviceType = "climate_sensor"
	DeviceTypeOccupancySensor DeviceType = "occupancy_sensor"
	DeviceTypeContactSensor   DeviceType = "contact_sensor"
	DeviceTypeLeakSensor      DeviceType = "leak_sensor"
	DeviceTypeSmokeSensor     DeviceType = "smoke_sensor"
	DeviceTypeLightbulb       DeviceType = "lightbulb"
	DeviceTypeOutlet          DeviceType = "outlet"
	DeviceTypeSwitch          DeviceType = "switch"
	DeviceTypeFan             DeviceType = "fan"
)

// DeviceFeatures indicates optional features of a device.
type DeviceFeatures struct {
	// Sensors
	Temperature bool `json:"temperature,omitempty"`
	Humidity    bool `json:"humidity,omitempty"`
	Battery     bool `json:"battery,omitempty"`
	Occupancy   bool `json:"occupancy,omitempty"`
	Illuminance bool `json:"illuminance,omitempty"`
	Pressure    bool `json:"pressure,omitempty"`
	Contact     bool `json:"contact,omitempty"`     // Door/window contact
	WaterLeak   bool `json:"water_leak,omitempty"`  // Water leak detection
	Smoke       bool `json:"smoke,omitempty"`       // Smoke detection
	Tamper      bool `json:"tamper,omitempty"`      // Tamper detection

	// Lights
	Brightness       bool `json:"brightness,omitempty"`
	Color            bool `json:"color,omitempty"`             // HSV color
	ColorTemperature bool `json:"color_temperature,omitempty"` // CT in mireds

	// Fans
	Speed     bool `json:"speed,omitempty"`     // Fan speed (0-100)
	Direction bool `json:"direction,omitempty"` // Rotation direction
	Swing     bool `json:"swing,omitempty"`     // Oscillation/swing mode
}

// Device describes a single Zigbee device.
type Device struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Topic    string         `json:"topic"` // zigbee2mqtt topic suffix
	Type     DeviceType     `json:"type"`
	Features DeviceFeatures `json:"features,omitempty"`
	HomeKit  *bool          `json:"homekit,omitempty"` // default true
	Web      *bool          `json:"web,omitempty"`     // default true
}

// Config defines the device configuration file structure.
type Config struct {
	Devices []Device `json:"devices"`
}

// LoadConfig reads and validates the HuJSON device configuration file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read devices config file: %w", err)
	}

	standardized, err := hujson.Standardize(data)
	if err != nil {
		return nil, fmt.Errorf("failed to standardize HuJSON: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(standardized, &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal devices config: %w", err)
	}

	if len(cfg.Devices) == 0 {
		return nil, fmt.Errorf("no devices configured")
	}

	seenIDs := make(map[string]struct{}, len(cfg.Devices))

	for i, device := range cfg.Devices {
		if device.ID == "" {
			return nil, fmt.Errorf("device %d has no ID", i)
		}
		if device.Name == "" {
			return nil, fmt.Errorf("device %s has no name", device.ID)
		}
		if device.Topic == "" {
			return nil, fmt.Errorf("device %s has no topic", device.ID)
		}
		if device.Type == "" {
			return nil, fmt.Errorf("device %s has no type", device.ID)
		}
		if !isValidDeviceType(device.Type) {
			return nil, fmt.Errorf("device %s has invalid type %q", device.ID, device.Type)
		}
		if _, exists := seenIDs[device.ID]; exists {
			return nil, fmt.Errorf("duplicate device id %q", device.ID)
		}
		seenIDs[device.ID] = struct{}{}

		// Set defaults for HomeKit and Web if not specified
		if cfg.Devices[i].HomeKit == nil {
			defaultTrue := true
			cfg.Devices[i].HomeKit = &defaultTrue
		}
		if cfg.Devices[i].Web == nil {
			defaultTrue := true
			cfg.Devices[i].Web = &defaultTrue
		}
	}

	return &cfg, nil
}

func isValidDeviceType(t DeviceType) bool {
	switch t {
	case DeviceTypeClimateSensor, DeviceTypeOccupancySensor,
		DeviceTypeContactSensor, DeviceTypeLeakSensor, DeviceTypeSmokeSensor,
		DeviceTypeLightbulb, DeviceTypeOutlet, DeviceTypeSwitch, DeviceTypeFan:
		return true
	default:
		return false
	}
}

// State represents the runtime state of a device.
type State struct {
	ID   string
	Name string

	// Sensor values
	Temperature *float64
	Humidity    *float64
	Battery     *int
	Occupancy   *bool
	Illuminance *int
	Pressure    *float64
	Contact     *bool // true = closed, false = open (Z2M convention)
	WaterLeak   *bool // true = leak detected
	Smoke       *bool // true = smoke detected
	Tamper      *bool // true = tampered

	// Light values
	On         *bool
	Brightness *int     // 0-254 (Z2M scale, convert to 0-100 for HAP)
	Hue        *float64 // 0-360
	Saturation *float64 // 0-100
	ColorTemp  *int     // mireds

	// Fan values
	FanSpeed     *int  // 0-100 (percentage)
	FanDirection *bool // true = forward, false = reverse
	FanSwing     *bool // true = oscillating

	// Connectivity
	LinkQuality int
	LastUpdated time.Time
	LastSeen    time.Time
}

// StateChangedEvent is emitted when a device's state changes (from MQTT).
type StateChangedEvent struct {
	DeviceID      string
	State         State
	UpdatedFields []string
}

// CommandEvent requests a device command.
type CommandEvent struct {
	DeviceID   string
	On         *bool
	Brightness *int     // 0-100 (HAP scale, convert to 0-254 for Z2M)
	Hue        *float64 // 0-360
	Saturation *float64 // 0-100
	ColorTemp  *int     // mireds
}

// ErrorEvent is emitted when a device encounters an error.
type ErrorEvent struct {
	DeviceID string
	Error    error
}

// Z2M brightness (0-254) to HAP brightness (0-100).
func Z2MBrightnessToHAP(z2m int) int {
	if z2m <= 0 {
		return 0
	}
	if z2m >= 254 {
		return 100
	}
	return int(float64(z2m) * 100.0 / 254.0)
}

// HAP brightness (0-100) to Z2M brightness (0-254).
func HAPBrightnessToZ2M(hap int) int {
	if hap <= 0 {
		return 0
	}
	if hap >= 100 {
		return 254
	}
	return int(float64(hap) * 254.0 / 100.0)
}

// ClampColorTemp clamps color temperature to HAP valid range (140-500).
func ClampColorTemp(ct int) int {
	if ct < 140 {
		return 140
	}
	if ct > 500 {
		return 500
	}
	return ct
}

// Z2MStateToBool converts Z2M state string to bool.
func Z2MStateToBool(state string) bool {
	return state == "ON"
}

// BoolToZ2MState converts bool to Z2M state string.
func BoolToZ2MState(on bool) string {
	if on {
		return "ON"
	}
	return "OFF"
}

// Ptr helpers for creating pointers to values.
func Ptr[T any](v T) *T {
	return &v
}
