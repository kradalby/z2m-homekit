package z2mhomekit

import (
	"log/slog"
	"testing"
	"time"

	"github.com/kradalby/z2m-homekit/devices"
	"github.com/kradalby/z2m-homekit/events"
)

func newTestBus(t *testing.T) *events.Bus {
	t.Helper()

	bus, err := events.New(slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("events.New: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	return bus
}

func newTestDeviceManager(t *testing.T, bus *events.Bus, cfg ...devices.Device) *devices.Manager {
	t.Helper()

	dm, err := devices.NewManager(cfg, make(chan devices.CommandEvent, 1), bus, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	return dm
}

func report(t *testing.T, dm *devices.Manager, id string, fill func(*devices.Reading)) {
	t.Helper()

	var r devices.Reading
	fill(&r)
	if _, ok := dm.Update(id, r, time.Now()); !ok {
		t.Fatalf("Update(%s) = false", id)
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// A leak reported before HomeKit starts must reach it without waiting for the
// sensor's next report, which can be hours away.
func TestHAPSeedsFromSnapshot(t *testing.T) {
	bus := newTestBus(t)
	cfg := devices.Device{ID: "leak", Name: "Leak", Topic: "leak", Type: devices.DeviceTypeLeakSensor}
	dm := newTestDeviceManager(t, bus, cfg)

	report(t, dm, "leak", func(r *devices.Reading) { r.WaterLeak.Set(true) })

	hm := NewHAPManager([]devices.Device{cfg}, "bridge", make(chan devices.CommandEvent, 1), dm, bus, slog.New(slog.DiscardHandler))
	hm.Start(t.Context())

	if got := hm.accessories["leak"].Leak.LeakDetected.Value(); got != 1 {
		t.Errorf("LeakDetected = %d after Start, want 1", got)
	}
}

func TestHAPFollowsSnapshot(t *testing.T) {
	bus := newTestBus(t)
	cfg := []devices.Device{
		{ID: "door", Name: "Door", Topic: "door", Type: devices.DeviceTypeContactSensor},
		{
			ID: "lamp", Name: "Lamp", Topic: "lamp", Type: devices.DeviceTypeLightbulb,
			Features: devices.DeviceFeatures{Brightness: true},
		},
	}
	dm := newTestDeviceManager(t, bus, cfg...)

	hm := NewHAPManager(cfg, "bridge", make(chan devices.CommandEvent, 1), dm, bus, slog.New(slog.DiscardHandler))
	hm.Start(t.Context())

	report(t, dm, "door", func(r *devices.Reading) { r.Contact.Set(false) })
	report(t, dm, "lamp", func(r *devices.Reading) {
		r.On.Set(true)
		r.Brightness.Set(254)
	})

	door, lamp := hm.accessories["door"], hm.accessories["lamp"]
	eventually(t, "door open", func() bool { return door.Contact.ContactSensorState.Value() == 1 })
	eventually(t, "lamp on at full brightness", func() bool {
		return lamp.Lightbulb.On.Value() && lamp.Brightness.Value() == 100
	})
}
