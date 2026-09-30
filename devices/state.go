package devices

import (
	"time"

	"tailscale.com/types/opt"
)

// Reading holds the values carried by one zigbee2mqtt report. Fields the
// report did not mention are unset.
type Reading struct {
	// Sensors
	Temperature opt.Value[float64]
	Humidity    opt.Value[float64]
	Battery     opt.Value[int]
	Occupancy   opt.Value[bool]
	Illuminance opt.Value[int]
	Pressure    opt.Value[float64]
	Contact     opt.Value[bool] // true = closed, false = open (Z2M convention)
	WaterLeak   opt.Value[bool]
	Smoke       opt.Value[bool]
	Tamper      opt.Value[bool]

	// Lights, outlets, switches and fans
	On         opt.Value[bool]
	Brightness opt.Value[int]     // 0-254 (Z2M scale, convert to 0-100 for HAP)
	Hue        opt.Value[float64] // 0-360
	Saturation opt.Value[float64] // 0-100
	ColorTemp  opt.Value[int]     // mireds
	FanSpeed   opt.Value[int]     // 0-100 (percentage)

	LinkQuality opt.Value[int]
}

// State is the last known value of every field of a device. It holds no
// pointers, so a copy never aliases the manager's state.
type State struct {
	Reading
	LastSeen time.Time // zero until the first report
}

// Apply returns s with the fields set in r overwritten, as reported at seen.
func Apply(s State, r Reading, seen time.Time) State {
	merge(&s.Temperature, r.Temperature)
	merge(&s.Humidity, r.Humidity)
	merge(&s.Battery, r.Battery)
	merge(&s.Occupancy, r.Occupancy)
	merge(&s.Illuminance, r.Illuminance)
	merge(&s.Pressure, r.Pressure)
	merge(&s.Contact, r.Contact)
	merge(&s.WaterLeak, r.WaterLeak)
	merge(&s.Smoke, r.Smoke)
	merge(&s.Tamper, r.Tamper)
	merge(&s.On, r.On)
	merge(&s.Brightness, r.Brightness)
	merge(&s.Hue, r.Hue)
	merge(&s.Saturation, r.Saturation)
	merge(&s.ColorTemp, r.ColorTemp)
	merge(&s.FanSpeed, r.FanSpeed)
	merge(&s.LinkQuality, r.LinkQuality)
	s.LastSeen = seen

	return s
}

func merge[T any](dst *opt.Value[T], src opt.Value[T]) {
	if src.IsSet() {
		*dst = src
	}
}

// ParseZ2M extracts the fields this bridge understands from a decoded
// zigbee2mqtt device message.
func ParseZ2M(msg map[string]any) Reading {
	var r Reading

	num := func(key string) (float64, bool) {
		v, ok := msg[key].(float64)
		return v, ok
	}
	flag := func(key string) (bool, bool) {
		v, ok := msg[key].(bool)
		return v, ok
	}

	if v, ok := num("linkquality"); ok {
		r.LinkQuality.Set(int(v))
	}
	if v, ok := num("temperature"); ok {
		r.Temperature.Set(v)
	}
	if v, ok := num("humidity"); ok {
		r.Humidity.Set(v)
	}
	if v, ok := num("battery"); ok {
		r.Battery.Set(int(v))
	}
	if v, ok := flag("occupancy"); ok {
		r.Occupancy.Set(v)
	}
	if v, ok := num("illuminance"); ok {
		r.Illuminance.Set(int(v))
	}
	if v, ok := num("illuminance_lux"); ok {
		r.Illuminance.Set(int(v))
	}
	if v, ok := num("pressure"); ok {
		r.Pressure.Set(v)
	}
	if v, ok := flag("contact"); ok {
		r.Contact.Set(v)
	}
	if v, ok := flag("water_leak"); ok {
		r.WaterLeak.Set(v)
	}
	if v, ok := flag("smoke"); ok {
		r.Smoke.Set(v)
	}
	if v, ok := flag("tamper"); ok {
		r.Tamper.Set(v)
	}

	if v, ok := msg["state"].(string); ok {
		r.On.Set(Z2MStateToBool(v))
	}
	if v, ok := num("brightness"); ok {
		r.Brightness.Set(int(v))
	}
	if v, ok := num("color_temp"); ok {
		r.ColorTemp.Set(int(v))
	}
	if color, ok := msg["color"].(map[string]any); ok {
		if v, ok := color["hue"].(float64); ok {
			r.Hue.Set(v)
		}
		if v, ok := color["saturation"].(float64); ok {
			r.Saturation.Set(v)
		}
	}

	// Fans report on/off as "fan_state" and speed either as a percentage or
	// as a named mode.
	if v, ok := msg["fan_state"].(string); ok {
		r.On.Set(Z2MStateToBool(v))
	}
	if v, ok := num("fan_speed"); ok {
		r.FanSpeed.Set(int(v))
	}
	if mode, ok := msg["fan_mode"].(string); ok {
		r.FanSpeed.Set(fanModeSpeed(mode))
	}

	return r
}

func fanModeSpeed(mode string) int {
	switch mode {
	case "off":
		return 0
	case "low":
		return 33
	case "medium":
		return 66
	case "high":
		return 100
	default: // "auto" and anything unknown
		return 50
	}
}
