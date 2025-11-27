package z2mhomekit

import (
	"context"
	"hash/fnv"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/brutella/hap"
	"github.com/brutella/hap/accessory"
	"github.com/brutella/hap/characteristic"
	"github.com/brutella/hap/service"
	"github.com/kradalby/z2m-homekit/devices"
	"github.com/kradalby/z2m-homekit/events"
	"tailscale.com/util/eventbus"
)

func hashString(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

// AccessoryInfo holds an accessory and its type-specific data
type AccessoryInfo struct {
	Accessory  *accessory.A
	DeviceType devices.DeviceType
	DeviceID   string

	// Sensors
	Temperature *service.TemperatureSensor
	Humidity    *service.HumiditySensor
	Occupancy   *service.OccupancySensor
	Battery     *service.BatteryService
	Contact     *service.ContactSensor
	Leak        *service.LeakSensor
	Smoke       *service.SmokeSensor

	// Lights
	Lightbulb        *service.Lightbulb
	Brightness       *characteristic.Brightness
	Hue              *characteristic.Hue
	Saturation       *characteristic.Saturation
	ColorTemperature *characteristic.ColorTemperature

	// Outlets/Switches
	Outlet *service.Outlet

	// Fans
	Fan         *service.Fan
	FanRotation *characteristic.RotationSpeed
}

// HAPManager manages HomeKit accessories and their state synchronization
type HAPManager struct {
	bridge          *accessory.Bridge
	accessories     map[string]*AccessoryInfo
	accessoryOrder  []string
	commands        chan devices.CommandEvent
	deviceManager   *devices.Manager
	stateSubscriber *eventbus.Subscriber[events.StateUpdateEvent]
	eventBus        *events.Bus
	eventClient     *eventbus.Client
	logger          *slog.Logger

	// Runtime info
	server *hap.Server
	store  hap.Store

	// Stats
	incomingCommands atomic.Uint64
	outgoingUpdates  atomic.Uint64
	lastActivity     atomic.Int64
}

// NewHAPManager creates a new HAP manager with accessories for all devices
func NewHAPManager(
	deviceConfigs []devices.Device,
	bridgeName string,
	commands chan devices.CommandEvent,
	deviceManager *devices.Manager,
	bus *events.Bus,
	logger *slog.Logger,
) *HAPManager {
	client, err := bus.Client(events.ClientHAP)
	if err != nil {
		panic(err)
	}

	// Create bridge accessory
	bridge := accessory.NewBridge(accessory.Info{
		Name:         bridgeName,
		Manufacturer: "z2m-homekit",
		Model:        "Bridge",
		SerialNumber: "Z2MB001",
	})

	hm := &HAPManager{
		bridge:          bridge,
		accessories:     make(map[string]*AccessoryInfo),
		accessoryOrder:  make([]string, 0, len(deviceConfigs)),
		commands:        commands,
		deviceManager:   deviceManager,
		stateSubscriber: eventbus.Subscribe[events.StateUpdateEvent](client),
		eventBus:        bus,
		eventClient:     client,
		logger:          logger,
	}

	// Create accessory for each device
	for _, device := range deviceConfigs {
		// Skip devices that are not enabled for HomeKit
		if device.HomeKit != nil && !*device.HomeKit {
			logger.Info("Skipping device for HomeKit", "device_id", device.ID, "name", device.Name)
			continue
		}

		accInfo := hm.createAccessory(device)
		if accInfo != nil {
			hm.accessories[device.ID] = accInfo
			hm.accessoryOrder = append(hm.accessoryOrder, device.ID)
		}
	}

	return hm
}

func (hm *HAPManager) createAccessory(device devices.Device) *AccessoryInfo {
	info := accessory.Info{
		Name:         device.Name,
		Manufacturer: "Zigbee2MQTT",
		Model:        string(device.Type),
		SerialNumber: device.ID,
	}

	accInfo := &AccessoryInfo{
		DeviceType: device.Type,
		DeviceID:   device.ID,
	}

	switch device.Type {
	case devices.DeviceTypeClimateSensor:
		accInfo.Accessory = hm.createClimateSensor(info, device, accInfo)
	case devices.DeviceTypeOccupancySensor:
		accInfo.Accessory = hm.createOccupancySensor(info, device, accInfo)
	case devices.DeviceTypeContactSensor:
		accInfo.Accessory = hm.createContactSensor(info, device, accInfo)
	case devices.DeviceTypeLeakSensor:
		accInfo.Accessory = hm.createLeakSensor(info, device, accInfo)
	case devices.DeviceTypeSmokeSensor:
		accInfo.Accessory = hm.createSmokeSensor(info, device, accInfo)
	case devices.DeviceTypeLightbulb:
		accInfo.Accessory = hm.createLightbulb(info, device, accInfo)
	case devices.DeviceTypeOutlet, devices.DeviceTypeSwitch:
		accInfo.Accessory = hm.createOutlet(info, device, accInfo)
	case devices.DeviceTypeFan:
		accInfo.Accessory = hm.createFan(info, device, accInfo)
	default:
		hm.logger.Warn("Unknown device type", "device_id", device.ID, "type", device.Type)
		return nil
	}

	if accInfo.Accessory != nil {
		accInfo.Accessory.Id = hashString(device.ID)
		hm.logger.Info("Created HomeKit accessory",
			"device_id", device.ID,
			"name", device.Name,
			"type", device.Type,
			"id", hashString(device.ID),
		)
	}

	return accInfo
}

func (hm *HAPManager) createClimateSensor(info accessory.Info, device devices.Device, accInfo *AccessoryInfo) *accessory.A {
	a := accessory.New(info, accessory.TypeSensor)

	// Add temperature sensor if feature enabled
	if device.Features.Temperature {
		tempSensor := service.NewTemperatureSensor()
		a.AddS(tempSensor.S)
		accInfo.Temperature = tempSensor
	}

	// Add humidity sensor if feature enabled
	if device.Features.Humidity {
		humiditySensor := service.NewHumiditySensor()
		a.AddS(humiditySensor.S)
		accInfo.Humidity = humiditySensor
	}

	// Add battery service if feature enabled
	if device.Features.Battery {
		battery := service.NewBatteryService()
		a.AddS(battery.S)
		accInfo.Battery = battery
	}

	return a
}

func (hm *HAPManager) createOccupancySensor(info accessory.Info, device devices.Device, accInfo *AccessoryInfo) *accessory.A {
	a := accessory.New(info, accessory.TypeSensor)

	occupancySensor := service.NewOccupancySensor()
	a.AddS(occupancySensor.S)
	accInfo.Occupancy = occupancySensor

	// Add battery service if feature enabled
	if device.Features.Battery {
		battery := service.NewBatteryService()
		a.AddS(battery.S)
		accInfo.Battery = battery
	}

	return a
}

func (hm *HAPManager) createContactSensor(info accessory.Info, device devices.Device, accInfo *AccessoryInfo) *accessory.A {
	a := accessory.New(info, accessory.TypeSensor)

	contactSensor := service.NewContactSensor()
	a.AddS(contactSensor.S)
	accInfo.Contact = contactSensor

	// Add battery service if feature enabled
	if device.Features.Battery {
		battery := service.NewBatteryService()
		a.AddS(battery.S)
		accInfo.Battery = battery
	}

	return a
}

func (hm *HAPManager) createLeakSensor(info accessory.Info, device devices.Device, accInfo *AccessoryInfo) *accessory.A {
	a := accessory.New(info, accessory.TypeSensor)

	leakSensor := service.NewLeakSensor()
	a.AddS(leakSensor.S)
	accInfo.Leak = leakSensor

	// Add battery service if feature enabled
	if device.Features.Battery {
		battery := service.NewBatteryService()
		a.AddS(battery.S)
		accInfo.Battery = battery
	}

	return a
}

func (hm *HAPManager) createSmokeSensor(info accessory.Info, device devices.Device, accInfo *AccessoryInfo) *accessory.A {
	a := accessory.New(info, accessory.TypeSensor)

	smokeSensor := service.NewSmokeSensor()
	a.AddS(smokeSensor.S)
	accInfo.Smoke = smokeSensor

	// Add battery service if feature enabled
	if device.Features.Battery {
		battery := service.NewBatteryService()
		a.AddS(battery.S)
		accInfo.Battery = battery
	}

	return a
}

func (hm *HAPManager) createFan(info accessory.Info, device devices.Device, accInfo *AccessoryInfo) *accessory.A {
	a := accessory.New(info, accessory.TypeFan)

	fan := service.NewFan()
	a.AddS(fan.S)
	accInfo.Fan = fan

	deviceID := device.ID

	// Set up On handler
	fan.On.OnValueRemoteUpdate(func(on bool) {
		hm.logger.Info("HomeKit fan power command received", "device_id", deviceID, "on", on)
		hm.incomingCommands.Add(1)
		hm.lastActivity.Store(time.Now().Unix())

		hm.commands <- devices.CommandEvent{
			DeviceID: deviceID,
			On:       devices.Ptr(on),
		}
		hm.publishCommand(deviceID, events.CommandTypeSetPower, devices.Ptr(on), nil, nil, nil, nil)
	})

	// Add rotation speed if speed feature enabled
	if device.Features.Speed {
		rotationSpeed := characteristic.NewRotationSpeed()
		fan.AddC(rotationSpeed.C)
		accInfo.FanRotation = rotationSpeed

		rotationSpeed.OnValueRemoteUpdate(func(value float64) {
			speed := int(value)
			hm.logger.Info("HomeKit fan speed command received", "device_id", deviceID, "speed", speed)
			hm.incomingCommands.Add(1)
			hm.lastActivity.Store(time.Now().Unix())

			hm.commands <- devices.CommandEvent{
				DeviceID:   deviceID,
				Brightness: devices.Ptr(speed), // Reuse brightness field for fan speed
			}
			hm.publishCommand(deviceID, events.CommandTypeSetBrightness, nil, devices.Ptr(speed), nil, nil, nil)
		})
	}

	return a
}

func (hm *HAPManager) createLightbulb(info accessory.Info, device devices.Device, accInfo *AccessoryInfo) *accessory.A {
	a := accessory.New(info, accessory.TypeLightbulb)

	lightbulb := service.NewLightbulb()
	a.AddS(lightbulb.S)
	accInfo.Lightbulb = lightbulb

	deviceID := device.ID

	// Set up On handler
	lightbulb.On.OnValueRemoteUpdate(func(on bool) {
		hm.logger.Info("HomeKit power command received", "device_id", deviceID, "on", on)
		hm.incomingCommands.Add(1)
		hm.lastActivity.Store(time.Now().Unix())

		hm.commands <- devices.CommandEvent{
			DeviceID: deviceID,
			On:       devices.Ptr(on),
		}
		hm.publishCommand(deviceID, events.CommandTypeSetPower, devices.Ptr(on), nil, nil, nil, nil)
	})

	// Add brightness if feature enabled
	if device.Features.Brightness {
		brightness := characteristic.NewBrightness()
		lightbulb.AddC(brightness.C)
		accInfo.Brightness = brightness

		brightness.OnValueRemoteUpdate(func(value int) {
			hm.logger.Info("HomeKit brightness command received", "device_id", deviceID, "brightness", value)
			hm.incomingCommands.Add(1)
			hm.lastActivity.Store(time.Now().Unix())

			hm.commands <- devices.CommandEvent{
				DeviceID:   deviceID,
				Brightness: devices.Ptr(value),
			}
			hm.publishCommand(deviceID, events.CommandTypeSetBrightness, nil, devices.Ptr(value), nil, nil, nil)
		})
	}

	// Add color if feature enabled
	if device.Features.Color {
		hue := characteristic.NewHue()
		saturation := characteristic.NewSaturation()
		lightbulb.AddC(hue.C)
		lightbulb.AddC(saturation.C)
		accInfo.Hue = hue
		accInfo.Saturation = saturation

		hue.OnValueRemoteUpdate(func(value float64) {
			hm.logger.Info("HomeKit hue command received", "device_id", deviceID, "hue", value)
			hm.incomingCommands.Add(1)
			hm.lastActivity.Store(time.Now().Unix())

			// Get current saturation
			currentSat := saturation.Value()
			hm.commands <- devices.CommandEvent{
				DeviceID:   deviceID,
				Hue:        devices.Ptr(value),
				Saturation: devices.Ptr(currentSat),
			}
			hm.publishCommand(deviceID, events.CommandTypeSetColor, nil, nil, devices.Ptr(value), devices.Ptr(currentSat), nil)
		})

		saturation.OnValueRemoteUpdate(func(value float64) {
			hm.logger.Info("HomeKit saturation command received", "device_id", deviceID, "saturation", value)
			hm.incomingCommands.Add(1)
			hm.lastActivity.Store(time.Now().Unix())

			// Get current hue
			currentHue := hue.Value()
			hm.commands <- devices.CommandEvent{
				DeviceID:   deviceID,
				Hue:        devices.Ptr(currentHue),
				Saturation: devices.Ptr(value),
			}
			hm.publishCommand(deviceID, events.CommandTypeSetColor, nil, nil, devices.Ptr(currentHue), devices.Ptr(value), nil)
		})
	}

	// Add color temperature if feature enabled
	if device.Features.ColorTemperature {
		colorTemp := characteristic.NewColorTemperature()
		lightbulb.AddC(colorTemp.C)
		accInfo.ColorTemperature = colorTemp

		colorTemp.OnValueRemoteUpdate(func(value int) {
			hm.logger.Info("HomeKit color temp command received", "device_id", deviceID, "color_temp", value)
			hm.incomingCommands.Add(1)
			hm.lastActivity.Store(time.Now().Unix())

			hm.commands <- devices.CommandEvent{
				DeviceID:  deviceID,
				ColorTemp: devices.Ptr(value),
			}
			hm.publishCommand(deviceID, events.CommandTypeSetColorTemp, nil, nil, nil, nil, devices.Ptr(value))
		})
	}

	return a
}

func (hm *HAPManager) createOutlet(info accessory.Info, device devices.Device, accInfo *AccessoryInfo) *accessory.A {
	outlet := accessory.NewOutlet(info)
	accInfo.Outlet = outlet.Outlet

	deviceID := device.ID

	outlet.Outlet.On.OnValueRemoteUpdate(func(on bool) {
		hm.logger.Info("HomeKit power command received", "device_id", deviceID, "on", on)
		hm.incomingCommands.Add(1)
		hm.lastActivity.Store(time.Now().Unix())

		hm.commands <- devices.CommandEvent{
			DeviceID: deviceID,
			On:       devices.Ptr(on),
		}
		hm.publishCommand(deviceID, events.CommandTypeSetPower, devices.Ptr(on), nil, nil, nil, nil)
	})

	return outlet.A
}

// GetAccessories returns all accessories for the HAP server
func (hm *HAPManager) GetAccessories() []*accessory.A {
	var accessories []*accessory.A
	accessories = append(accessories, hm.bridge.A)
	for _, deviceID := range hm.accessoryOrder {
		accInfo, ok := hm.accessories[deviceID]
		if !ok || accInfo.Accessory == nil {
			continue
		}
		accessories = append(accessories, accInfo.Accessory)
	}
	return accessories
}

// UpdateState updates the HomeKit state for a device
//
//nolint:errcheck // HAP characteristic SetValue errors are not actionable here
func (hm *HAPManager) UpdateState(event events.StateUpdateEvent) {
	accInfo, exists := hm.accessories[event.DeviceID]
	if !exists {
		hm.logger.Debug("Accessory not found for device", "device_id", event.DeviceID)
		return
	}

	// Update sensor values
	if accInfo.Temperature != nil && event.Temperature != nil {
		accInfo.Temperature.CurrentTemperature.SetValue(*event.Temperature)
	}

	if accInfo.Humidity != nil && event.Humidity != nil {
		accInfo.Humidity.CurrentRelativeHumidity.SetValue(*event.Humidity)
	}

	if accInfo.Occupancy != nil && event.Occupancy != nil {
		val := 0
		if *event.Occupancy {
			val = 1
		}
		accInfo.Occupancy.OccupancyDetected.SetValue(val)
	}

	if accInfo.Battery != nil && event.Battery != nil {
		accInfo.Battery.BatteryLevel.SetValue(*event.Battery)
		// Set low battery status
		lowBattery := 0
		if *event.Battery < 20 {
			lowBattery = 1
		}
		accInfo.Battery.StatusLowBattery.SetValue(lowBattery)
	}

	// Update contact sensor (door/window)
	// Z2M: true = closed, false = open
	// HAP: 0 = DETECTED (closed), 1 = NOT_DETECTED (open)
	if accInfo.Contact != nil && event.Contact != nil {
		val := 1 // Open (not detected)
		if *event.Contact {
			val = 0 // Closed (detected)
		}
		accInfo.Contact.ContactSensorState.SetValue(val)
	}

	// Update leak sensor
	// HAP: 0 = NOT_DETECTED, 1 = DETECTED
	if accInfo.Leak != nil && event.WaterLeak != nil {
		val := 0
		if *event.WaterLeak {
			val = 1
		}
		accInfo.Leak.LeakDetected.SetValue(val)
	}

	// Update smoke sensor
	// HAP: 0 = NOT_DETECTED, 1 = DETECTED
	if accInfo.Smoke != nil && event.Smoke != nil {
		val := 0
		if *event.Smoke {
			val = 1
		}
		accInfo.Smoke.SmokeDetected.SetValue(val)
	}

	// Update light values
	if accInfo.Lightbulb != nil && event.On != nil {
		accInfo.Lightbulb.On.SetValue(*event.On)
	}

	// Update outlet values
	if accInfo.Outlet != nil && event.On != nil {
		accInfo.Outlet.On.SetValue(*event.On)
	}

	if accInfo.Brightness != nil && event.Brightness != nil {
		accInfo.Brightness.SetValue(*event.Brightness)
	}

	if accInfo.Hue != nil && event.Hue != nil {
		accInfo.Hue.SetValue(*event.Hue)
	}

	if accInfo.Saturation != nil && event.Saturation != nil {
		accInfo.Saturation.SetValue(*event.Saturation)
	}

	if accInfo.ColorTemperature != nil && event.ColorTemp != nil {
		accInfo.ColorTemperature.SetValue(devices.ClampColorTemp(*event.ColorTemp))
	}

	// Update fan values
	if accInfo.Fan != nil && event.On != nil {
		accInfo.Fan.On.SetValue(*event.On)
	}

	if accInfo.FanRotation != nil && event.FanSpeed != nil {
		accInfo.FanRotation.SetValue(float64(*event.FanSpeed))
	}

	hm.outgoingUpdates.Add(1)
	hm.lastActivity.Store(time.Now().Unix())

	hm.logger.Debug("Updated HomeKit state",
		"device_id", event.DeviceID,
	)
}

// Start begins processing state changes.
func (hm *HAPManager) Start(ctx context.Context) {
	go hm.ProcessStateChanges(ctx)
}

// Close releases subscriptions.
func (hm *HAPManager) Close() {
	hm.stateSubscriber.Close()
}

func (hm *HAPManager) SetServer(s *hap.Server) {
	hm.server = s
}

func (hm *HAPManager) SetStore(s hap.Store) {
	hm.store = s
}

func (hm *HAPManager) ProcessStateChanges(ctx context.Context) {
	for {
		select {
		case event := <-hm.stateSubscriber.Events():
			hm.logger.Debug("Received state update event", "device_id", event.DeviceID)
			hm.UpdateState(event)
		case <-ctx.Done():
			return
		}
	}
}

func (hm *HAPManager) publishCommand(
	deviceID string,
	cmdType events.CommandType,
	on *bool,
	brightness *int,
	hue, saturation *float64,
	colorTemp *int,
) {
	if hm.eventBus == nil || hm.eventClient == nil {
		return
	}

	hm.eventBus.PublishCommand(hm.eventClient, events.CommandEvent{
		Timestamp:   time.Now(),
		Source:      "homekit",
		DeviceID:    deviceID,
		CommandType: cmdType,
		On:          on,
		Brightness:  brightness,
		Hue:         hue,
		Saturation:  saturation,
		ColorTemp:   colorTemp,
	})
}

// Stats returns HAP manager statistics
func (hm *HAPManager) Stats() (incomingCommands, outgoingUpdates uint64, lastActivity time.Time) {
	incomingCommands = hm.incomingCommands.Load()
	outgoingUpdates = hm.outgoingUpdates.Load()
	ts := hm.lastActivity.Load()
	if ts > 0 {
		lastActivity = time.Unix(ts, 0)
	}
	return
}
