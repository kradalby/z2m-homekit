package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"tailscale.com/util/eventbus"

	"github.com/kradalby/z2m-homekit/devices"
	"github.com/kradalby/z2m-homekit/events"
)

// Collector subscribes to eventbus updates and exposes Prometheus metrics.
type Collector struct {
	logger         *slog.Logger
	statusSub      *eventbus.Subscriber[events.ConnectionStatusEvent]
	commandSub     *eventbus.Subscriber[events.CommandEvent]
	statusGauge    *prometheus.GaugeVec
	commandCounter *prometheus.CounterVec
	ctx            context.Context
	cancel         context.CancelFunc
	shutdownOnce   sync.Once
	workers        sync.WaitGroup
}

// SnapshotSource provides the current state of every device.
type SnapshotSource interface {
	Snapshot() *devices.Snapshot
}

// NewCollector wires eventbus subscribers and device state into Prometheus
// metrics.
func NewCollector(ctx context.Context, logger *slog.Logger, bus *events.Bus, devs SnapshotSource, reg prometheus.Registerer) (*Collector, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}
	if bus == nil {
		return nil, fmt.Errorf("event bus is required")
	}
	if devs == nil {
		return nil, fmt.Errorf("device snapshot source is required")
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

	statusGauge := promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
		Name: "z2m_homekit_component_status",
		Help: "Lifecycle state per component (1 when matching status, 0 otherwise)",
	}, []string{"component", "status"})

	commandCounter := promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
		Name: "z2m_homekit_command_total",
		Help: "Total control commands by source and device",
	}, []string{"source", "device_id", "command_type"})

	reg.MustRegister(deviceCollector{devs})

	c := &Collector{
		logger:         logger,
		statusSub:      statusSub,
		commandSub:     commandSub,
		statusGauge:    statusGauge,
		commandCounter: commandCounter,
		ctx:            collectorCtx,
		cancel:         cancel,
	}

	c.workers.Go(c.consumeStatuses)
	c.workers.Go(c.consumeCommands)

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
		c.workers.Wait()
		c.logger.Info("metrics collector stopped")
	})
}

func (c *Collector) consumeStatuses() {
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
	for {
		select {
		case evt := <-c.commandSub.Events():
			c.observeCommand(evt)
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

var deviceStateDesc = prometheus.NewDesc(
	"z2m_homekit_device_state",
	"Device state values (temperature, humidity, battery, etc.)",
	[]string{"device_id", "name", "metric"}, nil,
)

// deviceCollector reads device state at scrape time, so it can never lag
// the snapshot or miss a change.
type deviceCollector struct {
	devs SnapshotSource
}

func (c deviceCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- deviceStateDesc
}

func (c deviceCollector) Collect(ch chan<- prometheus.Metric) {
	for _, ds := range c.devs.Snapshot().All() {
		emit := func(metric string, v float64) {
			ch <- prometheus.MustNewConstMetric(deviceStateDesc, prometheus.GaugeValue, v, ds.Device.ID, ds.Device.Name, metric)
		}
		st := ds.State

		if v, ok := st.Temperature.GetOk(); ok {
			emit("temperature", v)
		}
		if v, ok := st.Humidity.GetOk(); ok {
			emit("humidity", v)
		}
		if v, ok := st.Battery.GetOk(); ok {
			emit("battery", float64(v))
		}
		if v, ok := st.Occupancy.GetOk(); ok {
			emit("occupancy", boolGauge(v))
		}
		if v, ok := st.Illuminance.GetOk(); ok {
			emit("illuminance", float64(v))
		}
		if v, ok := st.Pressure.GetOk(); ok {
			emit("pressure", v)
		}
		if v, ok := st.Contact.GetOk(); ok {
			emit("contact", boolGauge(v)) // 1 = closed
		}
		if v, ok := st.WaterLeak.GetOk(); ok {
			emit("water_leak", boolGauge(v))
		}
		if v, ok := st.Smoke.GetOk(); ok {
			emit("smoke", boolGauge(v))
		}
		if v, ok := st.On.GetOk(); ok {
			emit("power", boolGauge(v))
		}
		if v, ok := st.Brightness.GetOk(); ok {
			emit("brightness", float64(devices.Z2MBrightnessToHAP(v))) // 0-100
		}
		if v, ok := st.FanSpeed.GetOk(); ok {
			emit("fan_speed", float64(v))
		}
		if v, ok := st.LinkQuality.GetOk(); ok {
			emit("link_quality", float64(v))
		}
	}
}

func boolGauge(b bool) float64 {
	if b {
		return 1
	}

	return 0
}
