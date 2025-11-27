package z2mhomekit

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/kradalby/z2m-homekit/devices"
	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
	"tailscale.com/util/eventbus"
)

// MQTTHook handles MQTT messages from zigbee2mqtt.
type MQTTHook struct {
	mqtt.HookBase
	statePublisher *eventbus.Publisher[devices.StateChangedEvent]
	deviceManager  *devices.Manager
	logger         *slog.Logger
}

// ID returns the hook identifier.
func (h *MQTTHook) ID() string {
	return "z2m-mqtt-hook"
}

// Provides returns the hook methods this hook provides.
func (h *MQTTHook) Provides(b byte) bool {
	return bytes.Contains([]byte{
		mqtt.OnConnect,
		mqtt.OnDisconnect,
		mqtt.OnPublish,
		mqtt.OnPublished,
	}, []byte{b})
}

// OnConnect is called when a client connects.
func (h *MQTTHook) OnConnect(cl *mqtt.Client, pk packets.Packet) error {
	clientID := cl.ID
	h.logger.Info("MQTT client connected", "client_id", clientID)
	return nil
}

// OnDisconnect is called when a client disconnects.
func (h *MQTTHook) OnDisconnect(cl *mqtt.Client, err error, expire bool) {
	clientID := cl.ID
	h.logger.Info("MQTT client disconnected", "client_id", clientID, "error", err, "expire", expire)
}

// OnPublish is called when a message is received from a client.
func (h *MQTTHook) OnPublish(cl *mqtt.Client, pk packets.Packet) (packets.Packet, error) {
	topic := pk.TopicName
	payload := pk.Payload

	h.logger.Debug("MQTT message received",
		"topic", topic,
		"payload", string(payload),
	)

	// Skip processing for non-zigbee2mqtt topics
	if !strings.HasPrefix(topic, "zigbee2mqtt/") {
		return pk, nil
	}

	// Skip bridge topics
	if strings.HasPrefix(topic, "zigbee2mqtt/bridge/") {
		return pk, nil
	}

	// Skip set command topics (these are outgoing commands)
	if strings.HasSuffix(topic, "/set") || strings.HasSuffix(topic, "/get") {
		return pk, nil
	}

	// Extract device topic from path: zigbee2mqtt/<device-topic>
	deviceTopic := strings.TrimPrefix(topic, "zigbee2mqtt/")

	// Look up device by topic
	device, found := h.deviceManager.DeviceByTopic(deviceTopic)
	if !found {
		h.logger.Debug("Received message for unknown device", "topic", deviceTopic)
		return pk, nil
	}

	// Parse payload
	var msg map[string]interface{}
	if err := json.Unmarshal(payload, &msg); err != nil {
		h.logger.Debug("Failed to parse MQTT payload", "error", err)
		return pk, nil
	}

	// Create state update from message
	state, fields := h.parseZ2MMessage(device, msg)

	if len(fields) > 0 {
		h.logger.Debug("Publishing state change",
			"device_id", device.ID,
			"fields", fields,
		)

		h.statePublisher.Publish(devices.StateChangedEvent{
			DeviceID:      device.ID,
			State:         state,
			UpdatedFields: fields,
		})
	}

	return pk, nil
}

func (h *MQTTHook) parseZ2MMessage(device devices.Device, msg map[string]interface{}) (devices.State, []string) {
	now := time.Now()
	state := devices.State{
		ID:          device.ID,
		Name:        device.Name,
		LastSeen:    now,
		LastUpdated: now,
	}
	var fields []string

	// Parse link quality (always present)
	if lq, ok := msg["linkquality"].(float64); ok {
		state.LinkQuality = int(lq)
		fields = append(fields, "LinkQuality")
	}

	// Parse sensor values
	if temp, ok := msg["temperature"].(float64); ok {
		state.Temperature = &temp
		fields = append(fields, "Temperature")
	}

	if humidity, ok := msg["humidity"].(float64); ok {
		state.Humidity = &humidity
		fields = append(fields, "Humidity")
	}

	if battery, ok := msg["battery"].(float64); ok {
		b := int(battery)
		state.Battery = &b
		fields = append(fields, "Battery")
	}

	if occupancy, ok := msg["occupancy"].(bool); ok {
		state.Occupancy = &occupancy
		fields = append(fields, "Occupancy")
	}

	if illuminance, ok := msg["illuminance"].(float64); ok {
		i := int(illuminance)
		state.Illuminance = &i
		fields = append(fields, "Illuminance")
	}
	// Also check illuminance_lux variant
	if illuminance, ok := msg["illuminance_lux"].(float64); ok {
		i := int(illuminance)
		state.Illuminance = &i
		fields = append(fields, "Illuminance")
	}

	if pressure, ok := msg["pressure"].(float64); ok {
		state.Pressure = &pressure
		fields = append(fields, "Pressure")
	}

	// Parse contact sensor (door/window)
	// Z2M: true = closed, false = open
	if contact, ok := msg["contact"].(bool); ok {
		state.Contact = &contact
		fields = append(fields, "Contact")
	}

	// Parse water leak sensor
	if waterLeak, ok := msg["water_leak"].(bool); ok {
		state.WaterLeak = &waterLeak
		fields = append(fields, "WaterLeak")
	}

	// Parse smoke sensor
	if smoke, ok := msg["smoke"].(bool); ok {
		state.Smoke = &smoke
		fields = append(fields, "Smoke")
	}

	// Parse tamper detection
	if tamper, ok := msg["tamper"].(bool); ok {
		state.Tamper = &tamper
		fields = append(fields, "Tamper")
	}

	// Parse light values
	if stateStr, ok := msg["state"].(string); ok {
		on := devices.Z2MStateToBool(stateStr)
		state.On = &on
		fields = append(fields, "On")
		h.logger.Info("Device state updated from MQTT",
			"device_id", device.ID,
			"on", on,
		)
	}

	if brightness, ok := msg["brightness"].(float64); ok {
		b := int(brightness)
		state.Brightness = &b
		fields = append(fields, "Brightness")
	}

	if colorTemp, ok := msg["color_temp"].(float64); ok {
		ct := int(colorTemp)
		state.ColorTemp = &ct
		fields = append(fields, "ColorTemp")
	}

	// Parse color object
	if color, ok := msg["color"].(map[string]interface{}); ok {
		if hue, ok := color["hue"].(float64); ok {
			state.Hue = &hue
			fields = append(fields, "Hue")
		}
		if sat, ok := color["saturation"].(float64); ok {
			state.Saturation = &sat
			fields = append(fields, "Saturation")
		}
	}

	// Parse fan values
	// Z2M uses "fan_state" for on/off and "fan_mode" for speed
	if fanState, ok := msg["fan_state"].(string); ok {
		on := devices.Z2MStateToBool(fanState)
		state.On = &on
		fields = append(fields, "On")
	}

	// Fan speed as percentage (0-100)
	if fanSpeed, ok := msg["fan_speed"].(float64); ok {
		speed := int(fanSpeed)
		state.FanSpeed = &speed
		fields = append(fields, "FanSpeed")
	}

	// Fan mode can indicate speed levels
	if fanMode, ok := msg["fan_mode"].(string); ok {
		// Convert common fan modes to percentage
		var speed int
		switch fanMode {
		case "off":
			speed = 0
		case "low":
			speed = 33
		case "medium":
			speed = 66
		case "high":
			speed = 100
		case "auto":
			speed = 50 // Default auto to medium
		default:
			speed = 50
		}
		state.FanSpeed = &speed
		fields = append(fields, "FanSpeed")
	}

	// Always add connectivity fields
	fields = append(fields, "LastSeen", "LastUpdated")

	return state, fields
}
