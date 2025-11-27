package devices

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kradalby/z2m-homekit/events"
	mqtt "github.com/mochi-mqtt/server/v2"
	"tailscale.com/util/eventbus"
)

// Manager manages all Zigbee device state.
type Manager struct {
	devices          map[string]*Info
	states           map[string]*State
	mu               sync.RWMutex
	commands         chan CommandEvent
	statePublisher   *eventbus.Publisher[StateChangedEvent]
	errorPublisher   *eventbus.Publisher[ErrorEvent]
	stateSubscriber  *eventbus.Subscriber[StateChangedEvent]
	eventBus         *events.Bus
	stateEventClient *eventbus.Client
	mqttServer       *mqtt.Server
	logger           *slog.Logger
}

// Info holds the configuration for a device.
type Info struct {
	Config Device
}

// NewManager creates a new device manager.
func NewManager(
	deviceConfigs []Device,
	commands chan CommandEvent,
	bus *events.Bus,
	mqttServer *mqtt.Server,
	logger *slog.Logger,
) (*Manager, error) {
	client, err := bus.Client(events.ClientDeviceManager)
	if err != nil {
		return nil, fmt.Errorf("failed to get devicemanager eventbus client: %w", err)
	}

	dm := &Manager{
		devices:          make(map[string]*Info),
		states:           make(map[string]*State),
		commands:         commands,
		statePublisher:   eventbus.Publish[StateChangedEvent](client),
		errorPublisher:   eventbus.Publish[ErrorEvent](client),
		stateSubscriber:  eventbus.Subscribe[StateChangedEvent](client),
		eventBus:         bus,
		stateEventClient: client,
		mqttServer:       mqttServer,
		logger:           logger,
	}

	for _, deviceConfig := range deviceConfigs {
		dm.devices[deviceConfig.ID] = &Info{
			Config: deviceConfig,
		}

		dm.states[deviceConfig.ID] = &State{
			ID:          deviceConfig.ID,
			Name:        deviceConfig.Name,
			LastUpdated: time.Now(),
			LastSeen:    time.Time{},
		}

		dm.publishStateUpdate("initial", deviceConfig.ID, *dm.states[deviceConfig.ID])

		logger.Info("Initialized device",
			"id", deviceConfig.ID,
			"name", deviceConfig.Name,
			"type", deviceConfig.Type,
			"topic", deviceConfig.Topic,
		)
	}

	return dm, nil
}

// SetPower sets the power state of a device via MQTT.
func (dm *Manager) SetPower(ctx context.Context, deviceID string, on bool) error {
	info, exists := dm.devices[deviceID]
	if !exists {
		return fmt.Errorf("device %s not found", deviceID)
	}

	topic := fmt.Sprintf("zigbee2mqtt/%s/set", info.Config.Topic)
	payload := map[string]string{"state": BoolToZ2MState(on)}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal command: %w", err)
	}

	dm.logger.Info("Sending power command",
		"device_id", deviceID,
		"topic", topic,
		"on", on,
	)

	if err := dm.mqttServer.Publish(topic, data, false, 0); err != nil {
		dm.errorPublisher.Publish(ErrorEvent{
			DeviceID: deviceID,
			Error:    fmt.Errorf("failed to publish power command: %w", err),
		})
		return err
	}

	return nil
}

// SetBrightness sets the brightness of a light via MQTT.
func (dm *Manager) SetBrightness(ctx context.Context, deviceID string, brightness int) error {
	info, exists := dm.devices[deviceID]
	if !exists {
		return fmt.Errorf("device %s not found", deviceID)
	}

	topic := fmt.Sprintf("zigbee2mqtt/%s/set", info.Config.Topic)
	// Convert HAP brightness (0-100) to Z2M brightness (0-254)
	z2mBrightness := HAPBrightnessToZ2M(brightness)
	payload := map[string]interface{}{
		"brightness": z2mBrightness,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal command: %w", err)
	}

	dm.logger.Info("Sending brightness command",
		"device_id", deviceID,
		"topic", topic,
		"brightness_hap", brightness,
		"brightness_z2m", z2mBrightness,
	)

	if err := dm.mqttServer.Publish(topic, data, false, 0); err != nil {
		return fmt.Errorf("failed to publish brightness command: %w", err)
	}

	return nil
}

// SetColor sets the color of a light via MQTT.
func (dm *Manager) SetColor(ctx context.Context, deviceID string, hue, saturation float64) error {
	info, exists := dm.devices[deviceID]
	if !exists {
		return fmt.Errorf("device %s not found", deviceID)
	}

	topic := fmt.Sprintf("zigbee2mqtt/%s/set", info.Config.Topic)
	payload := map[string]interface{}{
		"color": map[string]interface{}{
			"hue":        hue,
			"saturation": saturation,
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal command: %w", err)
	}

	dm.logger.Info("Sending color command",
		"device_id", deviceID,
		"topic", topic,
		"hue", hue,
		"saturation", saturation,
	)

	if err := dm.mqttServer.Publish(topic, data, false, 0); err != nil {
		return fmt.Errorf("failed to publish color command: %w", err)
	}

	return nil
}

// SetColorTemp sets the color temperature of a light via MQTT.
func (dm *Manager) SetColorTemp(ctx context.Context, deviceID string, colorTemp int) error {
	info, exists := dm.devices[deviceID]
	if !exists {
		return fmt.Errorf("device %s not found", deviceID)
	}

	topic := fmt.Sprintf("zigbee2mqtt/%s/set", info.Config.Topic)
	payload := map[string]interface{}{
		"color_temp": colorTemp,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal command: %w", err)
	}

	dm.logger.Info("Sending color temp command",
		"device_id", deviceID,
		"topic", topic,
		"color_temp", colorTemp,
	)

	if err := dm.mqttServer.Publish(topic, data, false, 0); err != nil {
		return fmt.Errorf("failed to publish color temp command: %w", err)
	}

	return nil
}

// ProcessCommands handles command events from HAP/Web.
func (dm *Manager) ProcessCommands(ctx context.Context) {
	for {
		select {
		case cmd := <-dm.commands:
			dm.processCommand(ctx, cmd)
		case <-ctx.Done():
			return
		}
	}
}

func (dm *Manager) processCommand(ctx context.Context, cmd CommandEvent) {
	if cmd.On != nil {
		if err := dm.SetPower(ctx, cmd.DeviceID, *cmd.On); err != nil {
			dm.logger.Error("Failed to process power command",
				"device_id", cmd.DeviceID,
				"error", err,
			)
		}
	}
	if cmd.Brightness != nil {
		if err := dm.SetBrightness(ctx, cmd.DeviceID, *cmd.Brightness); err != nil {
			dm.logger.Error("Failed to process brightness command",
				"device_id", cmd.DeviceID,
				"error", err,
			)
		}
	}
	if cmd.Hue != nil && cmd.Saturation != nil {
		if err := dm.SetColor(ctx, cmd.DeviceID, *cmd.Hue, *cmd.Saturation); err != nil {
			dm.logger.Error("Failed to process color command",
				"device_id", cmd.DeviceID,
				"error", err,
			)
		}
	}
	if cmd.ColorTemp != nil {
		if err := dm.SetColorTemp(ctx, cmd.DeviceID, *cmd.ColorTemp); err != nil {
			dm.logger.Error("Failed to process color temp command",
				"device_id", cmd.DeviceID,
				"error", err,
			)
		}
	}
}

// ProcessStateEvents merges state change events from the eventbus (from MQTT hook).
func (dm *Manager) ProcessStateEvents(ctx context.Context) {
	for {
		select {
		case event := <-dm.stateSubscriber.Events():
			dm.mu.Lock()
			state, exists := dm.states[event.DeviceID]
			if !exists {
				dm.mu.Unlock()
				dm.logger.Warn("Received state event for unknown device", "device_id", event.DeviceID)
				continue
			}

			if len(event.UpdatedFields) > 0 {
				// Selective update based on what changed
				for _, field := range event.UpdatedFields {
					switch field {
					case "On":
						state.On = event.State.On
					case "Brightness":
						state.Brightness = event.State.Brightness
					case "Hue":
						state.Hue = event.State.Hue
					case "Saturation":
						state.Saturation = event.State.Saturation
					case "ColorTemp":
						state.ColorTemp = event.State.ColorTemp
					case "Temperature":
						state.Temperature = event.State.Temperature
					case "Humidity":
						state.Humidity = event.State.Humidity
					case "Battery":
						state.Battery = event.State.Battery
					case "Occupancy":
						state.Occupancy = event.State.Occupancy
					case "Illuminance":
						state.Illuminance = event.State.Illuminance
					case "Pressure":
						state.Pressure = event.State.Pressure
					case "Contact":
						state.Contact = event.State.Contact
					case "WaterLeak":
						state.WaterLeak = event.State.WaterLeak
					case "Smoke":
						state.Smoke = event.State.Smoke
					case "Tamper":
						state.Tamper = event.State.Tamper
					case "FanSpeed":
						state.FanSpeed = event.State.FanSpeed
					case "LinkQuality":
						state.LinkQuality = event.State.LinkQuality
					case "LastSeen":
						state.LastSeen = event.State.LastSeen
					case "LastUpdated":
						state.LastUpdated = event.State.LastUpdated
					}
				}
			}

			stateCopy := *state
			dm.mu.Unlock()

			dm.logger.Debug("Merged state from eventbus",
				"device_id", event.DeviceID,
				"updated_fields", event.UpdatedFields,
			)
			dm.publishStateUpdate("eventbus", event.DeviceID, stateCopy)

		case <-ctx.Done():
			return
		}
	}
}

// Snapshot returns a copy of all device configs and states.
func (dm *Manager) Snapshot() map[string]struct {
	Device Device
	State  State
} {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	result := make(map[string]struct {
		Device Device
		State  State
	}, len(dm.devices))

	for id, info := range dm.devices {
		state := dm.states[id]
		result[id] = struct {
			Device Device
			State  State
		}{
			Device: info.Config,
			State:  *state,
		}
	}

	return result
}

// Device returns the device info and state for the given ID.
func (dm *Manager) Device(deviceID string) (Device, State, bool) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	info, ok := dm.devices[deviceID]
	if !ok {
		return Device{}, State{}, false
	}

	state, ok := dm.states[deviceID]
	if !ok {
		return Device{}, State{}, false
	}

	return info.Config, *state, true
}

// DeviceByTopic returns the device info for the given topic.
func (dm *Manager) DeviceByTopic(topic string) (Device, bool) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	for _, info := range dm.devices {
		if info.Config.Topic == topic {
			return info.Config, true
		}
	}

	return Device{}, false
}

func (dm *Manager) publishStateUpdate(source, deviceID string, state State) {
	if dm.eventBus == nil || dm.stateEventClient == nil {
		return
	}

	info, ok := dm.devices[deviceID]
	name := deviceID
	if ok {
		name = info.Config.Name
	}

	connectionState, connectionNote := connectionStatus(state.LastSeen)

	// Convert brightness to HAP scale for events
	var brightnessHAP *int
	if state.Brightness != nil {
		b := Z2MBrightnessToHAP(*state.Brightness)
		brightnessHAP = &b
	}

	dm.eventBus.PublishStateUpdate(dm.stateEventClient, events.StateUpdateEvent{
		Timestamp:       time.Now(),
		Source:          source,
		DeviceID:        deviceID,
		Name:            name,
		On:              state.On,
		Brightness:      brightnessHAP,
		Hue:             state.Hue,
		Saturation:      state.Saturation,
		ColorTemp:       state.ColorTemp,
		Temperature:     state.Temperature,
		Humidity:        state.Humidity,
		Battery:         state.Battery,
		Occupancy:       state.Occupancy,
		Illuminance:     state.Illuminance,
		Pressure:        state.Pressure,
		Contact:         state.Contact,
		WaterLeak:       state.WaterLeak,
		Smoke:           state.Smoke,
		Tamper:          state.Tamper,
		FanSpeed:        state.FanSpeed,
		LinkQuality:     state.LinkQuality,
		LastSeen:        state.LastSeen,
		LastUpdated:     state.LastUpdated,
		ConnectionState: connectionState,
		ConnectionNote:  connectionNote,
	})
}

func connectionStatus(lastSeen time.Time) (string, string) {
	if lastSeen.IsZero() {
		return "disconnected", "Never seen"
	}

	since := time.Since(lastSeen)
	switch {
	case since < 30*time.Second:
		return "connected", fmt.Sprintf("Last seen: %s ago", since.Round(time.Second))
	case since < 60*time.Second:
		return "stale", fmt.Sprintf("Last seen: %s ago", since.Round(time.Second))
	default:
		return "disconnected", fmt.Sprintf("Last seen: %s ago", since.Round(time.Second))
	}
}
