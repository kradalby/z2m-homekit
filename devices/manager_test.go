package devices

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/packets"

	"github.com/kradalby/z2m-homekit/events"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newTestManager wires a Manager to an in-process broker and returns a channel
// carrying every payload published to zigbee2mqtt/<topic>/set.
func newTestManager(t *testing.T, device Device) (*Manager, <-chan []byte) {
	t.Helper()

	bus, err := events.New(testLogger())
	if err != nil {
		t.Fatalf("events.New: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	srv := mqtt.New(&mqtt.Options{InlineClient: true})
	t.Cleanup(func() { _ = srv.Close() })

	if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatalf("AddHook: %v", err)
	}

	published := make(chan []byte, 8)
	if err := srv.Subscribe("zigbee2mqtt/"+device.Topic+"/set", 1, func(_ *mqtt.Client, _ packets.Subscription, pk packets.Packet) {
		payload := make([]byte, len(pk.Payload))
		copy(payload, pk.Payload)
		published <- payload
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	dm, err := NewManager([]Device{device}, make(chan CommandEvent, 1), bus, srv, testLogger())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	return dm, published
}

func awaitPayload(t *testing.T, ch <-chan []byte) map[string]any {
	t.Helper()

	select {
	case raw := <-ch:
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal %q: %v", raw, err)
		}

		return got
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an MQTT publish")

		return nil
	}
}

// Fan speed used to be routed through CommandEvent.Brightness, which published
// a rescaled {"brightness": 0-254} that Z2M ignores for fans. It must go out as
// {"fan_speed": 0-100}, on the same scale HomeKit's RotationSpeed uses.
func TestSetFanSpeedPublishesFanSpeed(t *testing.T) {
	dm, published := newTestManager(t, Device{
		ID:       "fan1",
		Name:     "Fan",
		Topic:    "office-fan",
		Type:     DeviceTypeFan,
		Features: DeviceFeatures{Speed: true},
	})

	if err := dm.SetFanSpeed(context.Background(), "fan1", 42); err != nil {
		t.Fatalf("SetFanSpeed: %v", err)
	}

	got := awaitPayload(t, published)

	if _, ok := got["brightness"]; ok {
		t.Errorf("fan speed published a brightness key: %v", got)
	}

	speed, ok := got["fan_speed"].(float64)
	if !ok {
		t.Fatalf("no fan_speed in payload: %v", got)
	}

	if int(speed) != 42 {
		t.Errorf("fan_speed = %d, want 42 (must not be rescaled)", int(speed))
	}
}

func TestSetFanSpeedClamps(t *testing.T) {
	dm, published := newTestManager(t, Device{
		ID: "fan1", Name: "Fan", Topic: "office-fan",
		Type: DeviceTypeFan, Features: DeviceFeatures{Speed: true},
	})

	for _, tc := range []struct{ in, want int }{{-5, 0}, {150, 100}} {
		if err := dm.SetFanSpeed(context.Background(), "fan1", tc.in); err != nil {
			t.Fatalf("SetFanSpeed(%d): %v", tc.in, err)
		}

		got := awaitPayload(t, published)
		if speed, _ := got["fan_speed"].(float64); int(speed) != tc.want {
			t.Errorf("SetFanSpeed(%d) published %v, want %d", tc.in, got["fan_speed"], tc.want)
		}
	}
}

// Brightness must keep its 0-100 -> 0-254 rescale; only fans opt out.
func TestSetBrightnessRescalesToZ2M(t *testing.T) {
	dm, published := newTestManager(t, Device{
		ID: "light1", Name: "Light", Topic: "desk-light",
		Type: DeviceTypeLightbulb, Features: DeviceFeatures{Brightness: true},
	})

	if err := dm.SetBrightness(context.Background(), "light1", 100); err != nil {
		t.Fatalf("SetBrightness: %v", err)
	}

	got := awaitPayload(t, published)
	if b, _ := got["brightness"].(float64); int(b) != 254 {
		t.Errorf("brightness = %v, want 254", got["brightness"])
	}
}

func TestProcessCommandRoutesFanSpeed(t *testing.T) {
	dm, published := newTestManager(t, Device{
		ID: "fan1", Name: "Fan", Topic: "office-fan",
		Type: DeviceTypeFan, Features: DeviceFeatures{Speed: true},
	})

	speed := 66
	dm.processCommand(context.Background(), CommandEvent{DeviceID: "fan1", FanSpeed: &speed})

	got := awaitPayload(t, published)
	if s, _ := got["fan_speed"].(float64); int(s) != 66 {
		t.Errorf("fan_speed = %v, want 66", got["fan_speed"])
	}
}

func newSensorManager(t *testing.T, ids ...string) *Manager {
	t.Helper()

	bus, err := events.New(testLogger())
	if err != nil {
		t.Fatalf("events.New: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	var cfg []Device
	for _, id := range ids {
		cfg = append(cfg, Device{ID: id, Name: id, Topic: id, Type: DeviceTypeClimateSensor})
	}

	dm, err := NewManager(cfg, nil, bus, nil, testLogger())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	return dm
}

func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestUpdatePublishesNewSnapshot(t *testing.T) {
	dm := newSensorManager(t, "a", "b")

	before := dm.Snapshot()
	if got := len(before.All()); got != 2 {
		t.Fatalf("initial snapshot has %d devices, want 2", got)
	}

	var r Reading
	r.Temperature.Set(21)
	at := time.Unix(1000, 0)
	if _, ok := dm.Update("a", r, at); !ok {
		t.Fatal("Update(a) = false")
	}

	after := dm.Snapshot()
	if after.Version() != before.Version()+1 {
		t.Errorf("Version = %d, want %d", after.Version(), before.Version()+1)
	}
	if !closed(before.Changed()) {
		t.Error("old snapshot's Changed not closed")
	}
	if closed(after.Changed()) {
		t.Error("current snapshot's Changed already closed")
	}

	// Readers may hold a snapshot while the manager moves on.
	if a, _ := before.Get("a"); a.State.Temperature.IsSet() {
		t.Error("update leaked into the previous snapshot")
	}

	changed := after.Since(before.Version())
	if len(changed) != 1 || changed[0].Device.ID != "a" {
		t.Fatalf("Since = %+v, want only device a", changed)
	}
	if got := changed[0].State; got.Temperature.Get() != 21 || !got.LastSeen.Equal(at) {
		t.Errorf("state = %+v, want temperature 21 seen at %v", got, at)
	}

	if _, ok := dm.Update("nope", r, at); ok {
		t.Error("Update(unknown) = true")
	}
	if dm.Snapshot() != after {
		t.Error("update of an unknown device published a snapshot")
	}
}

// Writers race load-apply-store; without serialisation one overwrites the other.
func TestConcurrentUpdatesAreNotLost(t *testing.T) {
	ids := []string{"a", "b", "c", "d"}
	dm := newSensorManager(t, ids...)
	start := dm.Snapshot().Version()

	const n = 200
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() {
			for i := range n {
				var r Reading
				r.LinkQuality.Set(i + 1)
				dm.Update(id, r, time.Now())
			}
		})
	}
	wg.Wait()

	snap := dm.Snapshot()
	if want := start + uint64(n*len(ids)); snap.Version() != want {
		t.Errorf("Version = %d, want %d", snap.Version(), want)
	}
	for _, id := range ids {
		ds, _ := snap.Get(id)
		if got := ds.State.LinkQuality.Get(); got != n {
			t.Errorf("%s LinkQuality = %d, want %d", id, got, n)
		}
	}
}

// Every reader shares the published snapshot, so nothing a reader can reach
// may alias it.
func TestSnapshotReadersCannotMutate(t *testing.T) {
	st := reflect.TypeFor[Snapshot]()
	for i := range st.NumField() {
		if f := st.Field(i); f.IsExported() {
			t.Errorf("Snapshot.%s is exported; a reader could mutate the shared snapshot", f.Name)
		}
	}

	dm := newSensorManager(t, "a", "b")
	snap := dm.Snapshot()

	all := snap.All()
	all[0].State.LinkQuality.Set(99)
	ds, _ := snap.Get("a")
	ds.State.LinkQuality.Set(99)

	var r Reading
	r.Temperature.Set(21)
	dm.Update("b", r, time.Now())

	for name, s := range map[string]*Snapshot{"held": snap, "later": dm.Snapshot()} {
		if a, _ := s.Get("a"); a.State.LinkQuality.IsSet() {
			t.Errorf("%s snapshot: reader's write to device a leaked in", name)
		}
	}
}
