package z2mhomekit

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
	"tailscale.com/util/eventbus"

	"github.com/kradalby/z2m-homekit/devices"
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
	var msg map[string]any
	if err := json.Unmarshal(payload, &msg); err != nil {
		h.logger.Debug("Failed to parse MQTT payload", "error", err)
		return pk, nil
	}

	reading := devices.ParseZ2M(msg)
	if on, ok := reading.On.GetOk(); ok {
		h.logger.Info("Device state updated from MQTT", "device_id", device.ID, "on", on)
	}

	// Any report on the device topic counts as a sighting, even one carrying
	// no field we track.
	h.statePublisher.Publish(devices.StateChangedEvent{
		DeviceID: device.ID,
		Reading:  reading,
		At:       time.Now(),
	})

	return pk, nil
}
