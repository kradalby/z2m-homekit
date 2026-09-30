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
	"tailscale.com/util/eventbus"

	"github.com/kradalby/z2m-homekit/devices"
	"github.com/kradalby/z2m-homekit/events"
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
	bridge         *accessory.Bridge
	accessories    map[string]*AccessoryInfo
	accessoryOrder []string
	commands       chan devices.CommandEvent
	deviceManager  *devices.Manager
	eventBus       *events.Bus
	eventClient    *eventbus.Client
	logger         *slog.Logger

	// Runtime info
	ctx    context.Context
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
		bridge:         bridge,
		accessories:    make(map[string]*AccessoryInfo),
		accessoryOrder: make([]string, 0, len(deviceConfigs)),
		commands:       commands,
		deviceManager:  deviceManager,
		eventBus:       bus,
		eventClient:    client,
		logger:         logger,
		ctx:            context.Background(),
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
		hm.dispatch(events.CommandTypeSetPower, devices.CommandEvent{
			DeviceID: deviceID,
			On:       new(on),
		})
	})

	// Add rotation speed if speed feature enabled
	if device.Features.Speed {
		rotationSpeed := characteristic.NewRotationSpeed()
		fan.AddC(rotationSpeed.C)
		accInfo.FanRotation = rotationSpeed

		rotationSpeed.OnValueRemoteUpdate(func(value float64) {
			speed := int(value)
			hm.logger.Info("HomeKit fan speed command received", "device_id", deviceID, "speed", speed)
			hm.dispatch(events.CommandTypeSetFanSpeed, devices.CommandEvent{
				DeviceID: deviceID,
				FanSpeed: new(speed),
			})
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
		hm.dispatch(events.CommandTypeSetPower, devices.CommandEvent{
			DeviceID: deviceID,
			On:       new(on),
		})
	})

	// Add brightness if feature enabled
	if device.Features.Brightness {
		brightness := characteristic.NewBrightness()
		lightbulb.AddC(brightness.C)
		accInfo.Brightness = brightness

		brightness.OnValueRemoteUpdate(func(value int) {
			hm.logger.Info("HomeKit brightness command received", "device_id", deviceID, "brightness", value)
			hm.dispatch(events.CommandTypeSetBrightness, devices.CommandEvent{
				DeviceID:   deviceID,
				Brightness: new(value),
			})
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

			// Get current saturation
			currentSat := saturation.Value()
			hm.dispatch(events.CommandTypeSetColor, devices.CommandEvent{
				DeviceID:   deviceID,
				Hue:        new(value),
				Saturation: new(currentSat),
			})
		})

		saturation.OnValueRemoteUpdate(func(value float64) {
			hm.logger.Info("HomeKit saturation command received", "device_id", deviceID, "saturation", value)

			// Get current hue
			currentHue := hue.Value()
			hm.dispatch(events.CommandTypeSetColor, devices.CommandEvent{
				DeviceID:   deviceID,
				Hue:        new(currentHue),
				Saturation: new(value),
			})
		})
	}

	// Add color temperature if feature enabled
	if device.Features.ColorTemperature {
		colorTemp := characteristic.NewColorTemperature()
		lightbulb.AddC(colorTemp.C)
		accInfo.ColorTemperature = colorTemp

		colorTemp.OnValueRemoteUpdate(func(value int) {
			hm.logger.Info("HomeKit color temp command received", "device_id", deviceID, "color_temp", value)
			hm.dispatch(events.CommandTypeSetColorTemp, devices.CommandEvent{
				DeviceID:  deviceID,
				ColorTemp: new(value),
			})
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
		hm.dispatch(events.CommandTypeSetPower, devices.CommandEvent{
			DeviceID: deviceID,
			On:       new(on),
		})
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

// UpdateState mirrors a device's state into its accessory. Fields the device
// has never reported leave the characteristic at its default.
//
//nolint:errcheck // HAP characteristic SetValue errors are not actionable here
func (hm *HAPManager) UpdateState(deviceID string, st devices.State) {
	accInfo, exists := hm.accessories[deviceID]
	if !exists {
		hm.logger.Debug("Accessory not found for device", "device_id", deviceID)
		return
	}

	if v, ok := st.Temperature.GetOk(); ok && accInfo.Temperature != nil {
		accInfo.Temperature.CurrentTemperature.SetValue(v)
	}

	if v, ok := st.Humidity.GetOk(); ok && accInfo.Humidity != nil {
		accInfo.Humidity.CurrentRelativeHumidity.SetValue(v)
	}

	if v, ok := st.Occupancy.GetOk(); ok && accInfo.Occupancy != nil {
		accInfo.Occupancy.OccupancyDetected.SetValue(boolToInt(v))
	}

	if v, ok := st.Battery.GetOk(); ok && accInfo.Battery != nil {
		accInfo.Battery.BatteryLevel.SetValue(v)
		accInfo.Battery.StatusLowBattery.SetValue(boolToInt(v < 20))
	}

	// Z2M: true = closed. HAP: 0 = contact detected (closed), 1 = open.
	if v, ok := st.Contact.GetOk(); ok && accInfo.Contact != nil {
		accInfo.Contact.ContactSensorState.SetValue(boolToInt(!v))
	}

	// HAP: 0 = not detected, 1 = detected.
	if v, ok := st.WaterLeak.GetOk(); ok && accInfo.Leak != nil {
		accInfo.Leak.LeakDetected.SetValue(boolToInt(v))
	}

	if v, ok := st.Smoke.GetOk(); ok && accInfo.Smoke != nil {
		accInfo.Smoke.SmokeDetected.SetValue(boolToInt(v))
	}

	if v, ok := st.On.GetOk(); ok {
		if accInfo.Lightbulb != nil {
			accInfo.Lightbulb.On.SetValue(v)
		}
		if accInfo.Outlet != nil {
			accInfo.Outlet.On.SetValue(v)
		}
		if accInfo.Fan != nil {
			accInfo.Fan.On.SetValue(v)
		}
	}

	if v, ok := st.Brightness.GetOk(); ok && accInfo.Brightness != nil {
		accInfo.Brightness.SetValue(devices.Z2MBrightnessToHAP(v))
	}

	if v, ok := st.Hue.GetOk(); ok && accInfo.Hue != nil {
		accInfo.Hue.SetValue(v)
	}

	if v, ok := st.Saturation.GetOk(); ok && accInfo.Saturation != nil {
		accInfo.Saturation.SetValue(v)
	}

	if v, ok := st.ColorTemp.GetOk(); ok && accInfo.ColorTemperature != nil {
		accInfo.ColorTemperature.SetValue(devices.ClampColorTemp(v))
	}

	if v, ok := st.FanSpeed.GetOk(); ok && accInfo.FanRotation != nil {
		accInfo.FanRotation.SetValue(float64(v))
	}

	hm.outgoingUpdates.Add(1)
	hm.lastActivity.Store(time.Now().Unix())

	hm.logger.Debug("Updated HomeKit state", "device_id", deviceID)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}

	return 0
}

// Start seeds every accessory from the current snapshot, so reports that
// arrived before HomeKit started are not lost, then follows later changes.
func (hm *HAPManager) Start(ctx context.Context) {
	hm.ctx = ctx

	snap := hm.deviceManager.Snapshot()
	for _, ds := range snap.All() {
		hm.UpdateState(ds.Device.ID, ds.State)
	}

	go hm.follow(ctx, snap)
}

func (hm *HAPManager) follow(ctx context.Context, snap *devices.Snapshot) {
	for {
		select {
		case <-snap.Changed():
		case <-ctx.Done():
			return
		}

		next := hm.deviceManager.Snapshot()
		for _, ds := range next.Since(snap.Version()) {
			hm.UpdateState(ds.Device.ID, ds.State)
		}
		snap = next
	}
}

func (hm *HAPManager) SetServer(s *hap.Server) {
	hm.server = s
}

func (hm *HAPManager) SetStore(s hap.Store) {
	hm.store = s
}

// dispatch records a HomeKit-originated command, hands it to the device
// manager and mirrors it onto the event bus.
//
// The send is guarded by hm.ctx: devices.Manager.ProcessCommands stops draining
// hm.commands as soon as its context is cancelled, so a bare send would wedge
// the HAP connection goroutine that ran this callback for good.
func (hm *HAPManager) dispatch(cmdType events.CommandType, cmd devices.CommandEvent) {
	hm.incomingCommands.Add(1)
	hm.lastActivity.Store(time.Now().Unix())

	select {
	case hm.commands <- cmd:
	case <-hm.ctx.Done():
		hm.logger.Warn("Dropping HomeKit command, shutting down",
			"device_id", cmd.DeviceID,
			"command_type", string(cmdType),
		)

		return
	}

	if hm.eventBus == nil || hm.eventClient == nil {
		return
	}

	hm.eventBus.PublishCommand(hm.eventClient, events.CommandEvent{
		Timestamp:   time.Now(),
		Source:      "homekit",
		DeviceID:    cmd.DeviceID,
		CommandType: cmdType,
		On:          cmd.On,
		Brightness:  cmd.Brightness,
		FanSpeed:    cmd.FanSpeed,
		Hue:         cmd.Hue,
		Saturation:  cmd.Saturation,
		ColorTemp:   cmd.ColorTemp,
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
