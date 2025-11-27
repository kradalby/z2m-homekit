package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/kradalby/z2m-homekit/events"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"tailscale.com/util/eventbus"
)

// Collector subscribes to eventbus updates and exposes Prometheus metrics.
type Collector struct {
	logger         *slog.Logger
	statusSub      *eventbus.Subscriber[events.ConnectionStatusEvent]
	commandSub     *eventbus.Subscriber[events.CommandEvent]
	stateSub       *eventbus.Subscriber[events.StateUpdateEvent]
	statusGauge    *prometheus.GaugeVec
	commandCounter *prometheus.CounterVec
	deviceState    *prometheus.GaugeVec
	ctx            context.Context
	cancel         context.CancelFunc
	shutdownOnce   sync.Once
	workers        sync.WaitGroup
}

// NewCollector wires eventbus subscribers into Prometheus metrics.
func NewCollector(ctx context.Context, logger *slog.Logger, bus *events.Bus, reg prometheus.Registerer) (*Collector, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}
	if bus == nil {
		return nil, fmt.Errorf("event bus is required")
	}
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	client, err := bus.Client(events.ClientMetrics)
	if err != nil {
		return nil, fmt.Errorf("failed to get metrics client: %w", err)
	}

	collectorCtx, cancel := context.WithCancel(ctx)
	statusSub := eventbus.Subscribe[events.ConnectionStatusEvent](client)
	commandSub := eventbus.Subscribe[events.CommandEvent](client)
	stateSub := eventbus.Subscribe[events.StateUpdateEvent](client)

	statusGauge := promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
		Name: "z2m_homekit_component_status",
		Help: "Lifecycle state per component (1 when matching status, 0 otherwise)",
	}, []string{"component", "status"})

	commandCounter := promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
		Name: "z2m_homekit_command_total",
		Help: "Total control commands by source and device",
	}, []string{"source", "device_id", "command_type"})

	deviceState := promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
		Name: "z2m_homekit_device_state",
		Help: "Device state values (temperature, humidity, battery, etc.)",
	}, []string{"device_id", "name", "metric"})

	c := &Collector{
		logger:         logger,
		statusSub:      statusSub,
		commandSub:     commandSub,
		stateSub:       stateSub,
		statusGauge:    statusGauge,
		commandCounter: commandCounter,
		deviceState:    deviceState,
		ctx:            collectorCtx,
		cancel:         cancel,
	}

	c.workers.Add(3)
	go c.consumeStatuses()
	go c.consumeCommands()
	go c.consumeStates()

	logger.Info("metrics collector started")

	return c, nil
}

// Close stops the collector and releases subscribers.
func (c *Collector) Close() {
	c.shutdownOnce.Do(func() {
		c.cancel()
		if c.statusSub != nil {
			c.statusSub.Close()
		}
		if c.commandSub != nil {
			c.commandSub.Close()
		}
		if c.stateSub != nil {
			c.stateSub.Close()
		}
		c.workers.Wait()
		c.logger.Info("metrics collector stopped")
	})
}

func (c *Collector) consumeStatuses() {
	defer c.workers.Done()
	for {
		select {
		case evt := <-c.statusSub.Events():
			c.observeStatus(evt)
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *Collector) consumeCommands() {
	defer c.workers.Done()
	for {
		select {
		case evt := <-c.commandSub.Events():
			c.observeCommand(evt)
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *Collector) consumeStates() {
	defer c.workers.Done()
	for {
		select {
		case evt := <-c.stateSub.Events():
			c.observeState(evt)
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *Collector) observeStatus(evt events.ConnectionStatusEvent) {
	for _, status := range []events.ConnectionStatus{
		events.ConnectionStatusDisconnected,
		events.ConnectionStatusConnecting,
		events.ConnectionStatusConnected,
		events.ConnectionStatusReconnecting,
		events.ConnectionStatusFailed,
	} {
		value := 0.0
		if status == evt.Status {
			value = 1.0
		}
		c.statusGauge.WithLabelValues(evt.Component, string(status)).Set(value)
	}
}

func (c *Collector) observeCommand(evt events.CommandEvent) {
	commandType := string(evt.CommandType)
	if commandType == "" {
		commandType = "unknown"
	}
	source := evt.Source
	if source == "" {
		source = "unknown"
	}
	deviceID := evt.DeviceID
	if deviceID == "" {
		deviceID = "unknown"
	}
	c.commandCounter.WithLabelValues(source, deviceID, commandType).Inc()
}

func (c *Collector) observeState(evt events.StateUpdateEvent) {
	deviceID := evt.DeviceID
	name := evt.Name
	if name == "" {
		name = deviceID
	}

	// Temperature sensor
	if evt.Temperature != nil {
		c.deviceState.WithLabelValues(deviceID, name, "temperature").Set(*evt.Temperature)
	}

	// Humidity sensor
	if evt.Humidity != nil {
		c.deviceState.WithLabelValues(deviceID, name, "humidity").Set(*evt.Humidity)
	}

	// Battery level
	if evt.Battery != nil {
		c.deviceState.WithLabelValues(deviceID, name, "battery").Set(float64(*evt.Battery))
	}

	// Occupancy sensor (1 = occupied, 0 = clear)
	if evt.Occupancy != nil {
		val := 0.0
		if *evt.Occupancy {
			val = 1.0
		}
		c.deviceState.WithLabelValues(deviceID, name, "occupancy").Set(val)
	}

	// Illuminance
	if evt.Illuminance != nil {
		c.deviceState.WithLabelValues(deviceID, name, "illuminance").Set(float64(*evt.Illuminance))
	}

	// Pressure
	if evt.Pressure != nil {
		c.deviceState.WithLabelValues(deviceID, name, "pressure").Set(*evt.Pressure)
	}

	// Contact sensor (1 = closed, 0 = open)
	if evt.Contact != nil {
		val := 0.0
		if *evt.Contact {
			val = 1.0
		}
		c.deviceState.WithLabelValues(deviceID, name, "contact").Set(val)
	}

	// Water leak sensor (1 = leak, 0 = no leak)
	if evt.WaterLeak != nil {
		val := 0.0
		if *evt.WaterLeak {
			val = 1.0
		}
		c.deviceState.WithLabelValues(deviceID, name, "water_leak").Set(val)
	}

	// Smoke sensor (1 = smoke, 0 = clear)
	if evt.Smoke != nil {
		val := 0.0
		if *evt.Smoke {
			val = 1.0
		}
		c.deviceState.WithLabelValues(deviceID, name, "smoke").Set(val)
	}

	// Power state (1 = on, 0 = off)
	if evt.On != nil {
		val := 0.0
		if *evt.On {
			val = 1.0
		}
		c.deviceState.WithLabelValues(deviceID, name, "power").Set(val)
	}

	// Brightness (0-100)
	if evt.Brightness != nil {
		c.deviceState.WithLabelValues(deviceID, name, "brightness").Set(float64(*evt.Brightness))
	}

	// Fan speed (0-100)
	if evt.FanSpeed != nil {
		c.deviceState.WithLabelValues(deviceID, name, "fan_speed").Set(float64(*evt.FanSpeed))
	}

	// Link quality
	if evt.LinkQuality > 0 {
		c.deviceState.WithLabelValues(deviceID, name, "link_quality").Set(float64(evt.LinkQuality))
	}
}
