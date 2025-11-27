package metrics

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/kradalby/z2m-homekit/events"
	"github.com/prometheus/client_golang/prometheus"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestNewCollectorRequiresContext(t *testing.T) {
	bus, _ := events.New(testLogger())
	defer func() { _ = bus.Close() }()

	//nolint:staticcheck // SA1012: intentionally testing nil context handling
	_, err := NewCollector(nil, testLogger(), bus, nil)
	if err == nil {
		t.Error("expected error for nil context")
	}
}

func TestNewCollectorRequiresLogger(t *testing.T) {
	ctx := context.Background()
	bus, _ := events.New(testLogger())
	defer bus.Close()

	_, err := NewCollector(ctx, nil, bus, nil)
	if err == nil {
		t.Error("expected error for nil logger")
	}
}

func TestNewCollectorRequiresBus(t *testing.T) {
	ctx := context.Background()

	_, err := NewCollector(ctx, testLogger(), nil, nil)
	if err == nil {
		t.Error("expected error for nil bus")
	}
}

func TestNewCollectorSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bus, err := events.New(testLogger())
	if err != nil {
		t.Fatalf("failed to create bus: %v", err)
	}
	defer bus.Close()

	reg := prometheus.NewRegistry()
	collector, err := NewCollector(ctx, testLogger(), bus, reg)
	if err != nil {
		t.Fatalf("NewCollector() error = %v", err)
	}
	if collector == nil {
		t.Fatal("NewCollector() returned nil")
	}

	collector.Close()
}

func TestCollectorObservesStatusEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bus, err := events.New(testLogger())
	if err != nil {
		t.Fatalf("failed to create bus: %v", err)
	}
	defer bus.Close()

	reg := prometheus.NewRegistry()
	collector, err := NewCollector(ctx, testLogger(), bus, reg)
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

func TestCollectorObservesStateEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bus, err := events.New(testLogger())
	if err != nil {
		t.Fatalf("failed to create bus: %v", err)
	}
	defer bus.Close()

	reg := prometheus.NewRegistry()
	collector, err := NewCollector(ctx, testLogger(), bus, reg)
	if err != nil {
		t.Fatalf("NewCollector() error = %v", err)
	}
	defer collector.Close()

	// Get a client to publish events
	client, err := bus.Client(events.ClientMQTT)
	if err != nil {
		t.Fatalf("failed to get client: %v", err)
	}

	// Publish a state event
	temp := 22.5
	humidity := 50.0
	battery := 85
	bus.PublishStateUpdate(client, events.StateUpdateEvent{
		Timestamp:   time.Now(),
		DeviceID:    "test-sensor",
		Name:        "Test Sensor",
		Temperature: &temp,
		Humidity:    &humidity,
		Battery:     &battery,
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
		if family.GetName() == "z2m_homekit_device_state" {
			found = true
			// Check we have multiple metrics for different properties
			if len(family.GetMetric()) < 3 {
				t.Errorf("expected at least 3 metrics (temp, humidity, battery), got %d", len(family.GetMetric()))
			}
			break
		}
	}

	if !found {
		t.Error("expected z2m_homekit_device_state metric to be present")
	}
}
