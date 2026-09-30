package metrics

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kradalby/z2m-homekit/devices"
	"github.com/kradalby/z2m-homekit/events"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestNewCollectorRequiresContext(t *testing.T) {
	bus, _ := events.New(testLogger())
	defer func() { _ = bus.Close() }()

	//nolint:staticcheck // SA1012: intentionally testing nil context handling
	_, err := NewCollector(nil, testLogger(), bus, emptySource{}, nil)
	if err == nil {
		t.Error("expected error for nil context")
	}
}

func TestNewCollectorRequiresLogger(t *testing.T) {
	ctx := context.Background()
	bus, _ := events.New(testLogger())
	defer func() { _ = bus.Close() }()

	_, err := NewCollector(ctx, nil, bus, emptySource{}, nil)
	if err == nil {
		t.Error("expected error for nil logger")
	}
}

func TestNewCollectorRequiresBus(t *testing.T) {
	ctx := context.Background()

	_, err := NewCollector(ctx, testLogger(), nil, emptySource{}, nil)
	if err == nil {
		t.Error("expected error for nil bus")
	}
}

func TestNewCollectorRequiresSource(t *testing.T) {
	bus, _ := events.New(testLogger())
	defer func() { _ = bus.Close() }()

	_, err := NewCollector(t.Context(), testLogger(), bus, nil, nil)
	if err == nil {
		t.Error("expected error for nil snapshot source")
	}
}

func TestNewCollectorSuccess(t *testing.T) {
	ctx := t.Context()

	bus, err := events.New(testLogger())
	if err != nil {
		t.Fatalf("failed to create bus: %v", err)
	}
	defer func() { _ = bus.Close() }()

	reg := prometheus.NewRegistry()
	collector, err := NewCollector(ctx, testLogger(), bus, emptySource{}, reg)
	if err != nil {
		t.Fatalf("NewCollector() error = %v", err)
	}
	if collector == nil {
		t.Fatal("NewCollector() returned nil")
	}

	collector.Close()
}

func TestCollectorObservesStatusEvents(t *testing.T) {
	ctx := t.Context()

	bus, err := events.New(testLogger())
	if err != nil {
		t.Fatalf("failed to create bus: %v", err)
	}
	defer func() { _ = bus.Close() }()

	reg := prometheus.NewRegistry()
	collector, err := NewCollector(ctx, testLogger(), bus, emptySource{}, reg)
	if err != nil {
		t.Fatalf("NewCollector() error = %v", err)
	}
	defer collector.Close()

	// Get a client to publish events
	client, err := bus.Client(events.ClientMQTT)
	if err != nil {
		t.Fatalf("failed to get client: %v", err)
	}

	// Publish a status event
	bus.PublishConnectionStatus(client, events.ConnectionStatusEvent{
		Timestamp: time.Now(),
		Component: "mqtt",
		Status:    events.ConnectionStatusConnected,
	})

	// Give collector time to process
	time.Sleep(50 * time.Millisecond)

	// Verify metrics were recorded
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	found := false
	for _, family := range families {
		if family.GetName() == "z2m_homekit_component_status" {
			found = true
			break
		}
	}

	if !found {
		t.Error("expected z2m_homekit_component_status metric to be present")
	}
}

// Device gauges come from the snapshot at scrape time, so the latest report
// is what Prometheus sees.
func TestCollectorReportsDeviceSnapshot(t *testing.T) {
	bus, err := events.New(testLogger())
	if err != nil {
		t.Fatalf("failed to create bus: %v", err)
	}
	defer func() { _ = bus.Close() }()

	dm, err := devices.NewManager([]devices.Device{
		{ID: "sensor", Name: "Sensor", Topic: "sensor", Type: devices.DeviceTypeClimateSensor},
		{ID: "lamp", Name: "Lamp", Topic: "lamp", Type: devices.DeviceTypeLightbulb},
	}, nil, bus, nil, testLogger())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	reg := prometheus.NewRegistry()
	collector, err := NewCollector(t.Context(), testLogger(), bus, dm, reg)
	if err != nil {
		t.Fatalf("NewCollector() error = %v", err)
	}
	defer collector.Close()

	var r devices.Reading
	r.Temperature.Set(20)
	dm.Update("sensor", r, time.Now())
	r.Temperature.Set(22.5)
	r.Humidity.Set(50)
	dm.Update("sensor", r, time.Now())

	var l devices.Reading
	l.On.Set(true)
	l.Brightness.Set(254)
	dm.Update("lamp", l, time.Now())

	got := deviceGauges(t, reg)
	want := map[string]float64{
		"sensor/temperature": 22.5,
		"sensor/humidity":    50,
		"lamp/power":         1,
		"lamp/brightness":    100,
	}
	if len(got) != len(want) {
		t.Errorf("got %d gauges %v, want %v", len(got), got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}

type emptySource struct{}

func (emptySource) Snapshot() *devices.Snapshot { return &devices.Snapshot{} }

// deviceGauges returns z2m_homekit_device_state keyed by "device_id/metric".
func deviceGauges(t *testing.T, reg *prometheus.Registry) map[string]float64 {
	t.Helper()

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	out := map[string]float64{}
	for _, family := range families {
		if family.GetName() != "z2m_homekit_device_state" {
			continue
		}
		for _, m := range family.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			out[labels["device_id"]+"/"+labels["metric"]] = m.GetGauge().GetValue()
		}
	}

	return out
}
