package events

import (
	"log/slog"
	"os"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestBusClientNames(t *testing.T) {
	clients := []ClientName{
		ClientHAP,
		ClientMQTT,
		ClientWeb,
		ClientDeviceManager,
		ClientMetrics,
	}

	// Ensure all client names are unique
	seen := make(map[ClientName]bool)
	for _, c := range clients {
		if seen[c] {
			t.Errorf("Duplicate client name: %s", c)
		}
		seen[c] = true
	}
}

func TestNew(t *testing.T) {
	bus, err := New(testLogger())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if bus == nil {
		t.Fatal("New() returned nil")
	}
	defer func() { _ = bus.Close() }()
}

func TestNewRequiresLogger(t *testing.T) {
	_, err := New(nil)
	if err == nil {
		t.Error("New(nil) should return error")
	}
}

func TestBusClient(t *testing.T) {
	bus, err := New(testLogger())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = bus.Close() }()

	client, err := bus.Client(ClientHAP)
	if err != nil {
		t.Fatalf("Client() error = %v", err)
	}
	if client == nil {
		t.Fatal("Client() returned nil")
	}

	// Getting the same client should return the same instance
	client2, err := bus.Client(ClientHAP)
	if err != nil {
		t.Fatalf("Client() error = %v", err)
	}
	if client != client2 {
		t.Error("Client() returned different instance for same name")
	}
}

func TestBusClientUnknown(t *testing.T) {
	bus, err := New(testLogger())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = bus.Close() }()

	_, err = bus.Client("unknown-client")
	if err == nil {
		t.Error("Client() should error for unknown client")
	}
}

func TestStateUpdateEventEquals(t *testing.T) {
	temp1 := 22.5
	temp2 := 23.0
	humidity := 50.0

	tests := []struct {
		name  string
		a, b  StateUpdateEvent
		equal bool
	}{
		{
			name:  "same device same values",
			a:     StateUpdateEvent{DeviceID: "test", Temperature: &temp1},
			b:     StateUpdateEvent{DeviceID: "test", Temperature: &temp1},
			equal: true,
		},
		{
			name:  "same device different temperature",
			a:     StateUpdateEvent{DeviceID: "test", Temperature: &temp1},
			b:     StateUpdateEvent{DeviceID: "test", Temperature: &temp2},
			equal: false,
		},
		{
			name:  "different device",
			a:     StateUpdateEvent{DeviceID: "test1", Temperature: &temp1},
			b:     StateUpdateEvent{DeviceID: "test2", Temperature: &temp1},
			equal: false,
		},
		{
			name:  "one has humidity other doesn't",
			a:     StateUpdateEvent{DeviceID: "test", Temperature: &temp1, Humidity: &humidity},
			b:     StateUpdateEvent{DeviceID: "test", Temperature: &temp1},
			equal: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.a.Equals(tt.b)
			if got != tt.equal {
				t.Errorf("Equals() = %v, want %v", got, tt.equal)
			}
		})
	}
}
