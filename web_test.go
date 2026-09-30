package z2mhomekit

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kradalby/z2m-homekit/devices"
)

// eventLog is appended to from concurrent HTTP handlers and read by the index
// renderer. It was the one shared field on WebServer with no mutex; run this
// under -race to keep it that way.
func TestEventLogConcurrentAccess(t *testing.T) {
	ws := &WebServer{}

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 50 {
				ws.LogEvent(fmt.Sprintf("writer %d event %d", i, j))
			}
		})
		wg.Go(func() {
			for range 50 {
				for _, entry := range ws.recentEvents(20) {
					_ = entry
				}
			}
		})
	}
	wg.Wait()

	if got := len(ws.recentEvents(20)); got != 20 {
		t.Errorf("recentEvents(20) returned %d entries, want 20", got)
	}
}

func TestRecentEventsNewestFirstAndBounded(t *testing.T) {
	ws := &WebServer{}
	for i := range 5 {
		ws.LogEvent(fmt.Sprintf("e%d", i))
	}

	got := ws.recentEvents(3)
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}

	// Entries are timestamped, so match on the suffix we control.
	for i, want := range []string{"e4", "e3", "e2"} {
		if len(got[i]) < len(want) || got[i][len(got[i])-len(want):] != want {
			t.Errorf("entry %d = %q, want it to end in %q", i, got[i], want)
		}
	}

	if n := len(ws.recentEvents(100)); n != 5 {
		t.Errorf("recentEvents(100) = %d entries, want 5 (only 5 logged)", n)
	}
}

type sseEvent struct {
	name, data string
}

func newTestWebServer(t *testing.T, cfg ...devices.Device) (*WebServer, *devices.Manager) {
	t.Helper()

	bus := newTestBus(t)
	dm := newTestDeviceManager(t, bus, cfg...)
	ws := NewWebServer(slog.New(slog.DiscardHandler), dm, dm, bus, nil, "", "", nil)
	t.Cleanup(ws.Close)

	return ws, dm
}

// connectSSE opens /events against a real server so the stream can be read
// while the handler is still writing.
func connectSSE(t *testing.T, ws *WebServer) <-chan sseEvent {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(ws.HandleSSE))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}

	out := make(chan sseEvent)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		defer close(out)

		var ev sseEvent
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
				ev = sseEvent{}
			case strings.HasPrefix(line, "event: "):
				ev.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev.data += strings.TrimPrefix(line, "data: ")
			}
		}
	}()

	return out
}

// cards collects the latest card per SSE event until done reports true.
type cards map[string]string

func (c cards) await(t *testing.T, events <-chan sseEvent, what string, done func(cards) bool) {
	t.Helper()

	timeout := time.After(5 * time.Second)
	for !done(c) {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("stream closed waiting for %s", what)
			}
			c[ev.name] = ev.data
		case <-timeout:
			t.Fatalf("timed out waiting for %s; have %d cards", what, len(c))
		}
	}
}

// Once converged, nothing newer exists, so any further event could only be a
// stale or redundant card.
func expectQuiet(t *testing.T, events <-chan sseEvent) {
	t.Helper()

	select {
	case ev := <-events:
		t.Errorf("unexpected event after convergence: %s %s", ev.name, ev.data)
	case <-time.After(50 * time.Millisecond):
	}
}

func climateSensors(n int) []devices.Device {
	var cfg []devices.Device
	for i := range n {
		id := fmt.Sprintf("dev%02d", i)
		cfg = append(cfg, devices.Device{
			ID: id, Name: id, Topic: id, Type: devices.DeviceTypeClimateSensor,
			Features: devices.DeviceFeatures{Temperature: true},
		})
	}

	return cfg
}

func setTemp(t *testing.T, dm *devices.Manager, id string, temp float64) {
	t.Helper()
	report(t, dm, id, func(r *devices.Reading) { r.Temperature.Set(temp) })
}

func showsTemp(card string, temp float64) bool {
	return strings.Contains(card, fmt.Sprintf(">%.1f °C<", temp))
}

// A new client must get every device, however many there are.
func TestSSEReplaysEveryDevice(t *testing.T) {
	cfg := climateSensors(15)
	ws, dm := newTestWebServer(t, cfg...)
	for i, d := range cfg {
		setTemp(t, dm, d.ID, float64(i))
	}

	events := connectSSE(t, ws)
	got := cards{}
	got.await(t, events, "all devices", func(c cards) bool { return len(c) == len(cfg) })

	for i, d := range cfg {
		if !showsTemp(got[sseEventName(d.ID)], float64(i)) {
			t.Errorf("%s card = %s, want %.1f °C", d.ID, got[sseEventName(d.ID)], float64(i))
		}
	}
	expectQuiet(t, events)
}

// A chatty device must not crowd out a quiet one, and a burst across many
// devices must leave every card on its latest value.
func TestSSEBurstEndsOnLatestState(t *testing.T) {
	cfg := climateSensors(25)
	ws, dm := newTestWebServer(t, cfg...)

	events := connectSSE(t, ws)
	got := cards{}
	got.await(t, events, "initial cards", func(c cards) bool { return len(c) == len(cfg) })

	final := map[string]float64{}
	for round := range 20 {
		setTemp(t, dm, "dev00", float64(1000+round)) // chatty
		final["dev00"] = float64(1000 + round)
		for i, d := range cfg[1:] {
			if round == 0 || i%5 == round%5 {
				setTemp(t, dm, d.ID, float64(round*100+i))
				final[d.ID] = float64(round*100 + i)
			}
		}
	}

	got.await(t, events, "latest state everywhere", func(c cards) bool {
		for id, temp := range final {
			if !showsTemp(c[sseEventName(id)], temp) {
				return false
			}
		}
		return true
	})
	expectQuiet(t, events)
}

// Clients connecting while a device is changing must still end on its newest
// state, never on one that raced ahead of it.
func TestSSEConnectDuringUpdatesEndsOnLatest(t *testing.T) {
	cfg := climateSensors(1)
	ws, dm := newTestWebServer(t, cfg...)
	id := cfg[0].ID

	const clients, updates = 20, 200
	var streams []<-chan sseEvent

	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range updates {
			var r devices.Reading
			r.Temperature.Set(float64(i))
			dm.Update(id, r, time.Now())
		}
	})
	for range clients {
		streams = append(streams, connectSSE(t, ws))
	}
	wg.Wait()

	for _, events := range streams {
		got := cards{}
		got.await(t, events, "final temperature", func(c cards) bool {
			return showsTemp(c[sseEventName(id)], updates-1)
		})
		expectQuiet(t, events)
	}
}

// Contact, leak and smoke used to render only on page load; the live stream
// left a dashboard showing "No Leak" through an actual leak.
func TestSSECardShowsAlarmState(t *testing.T) {
	ws, dm := newTestWebServer(t,
		devices.Device{ID: "leak", Name: "Leak", Topic: "leak", Type: devices.DeviceTypeLeakSensor},
		devices.Device{ID: "smoke", Name: "Smoke", Topic: "smoke", Type: devices.DeviceTypeSmokeSensor},
		devices.Device{ID: "door", Name: "Door", Topic: "door", Type: devices.DeviceTypeContactSensor},
	)

	events := connectSSE(t, ws)
	got := cards{}
	got.await(t, events, "initial cards", func(c cards) bool { return len(c) == 3 })

	report(t, dm, "leak", func(r *devices.Reading) { r.WaterLeak.Set(true) })
	report(t, dm, "smoke", func(r *devices.Reading) { r.Smoke.Set(true) })
	report(t, dm, "door", func(r *devices.Reading) { r.Contact.Set(false) })

	got.await(t, events, "alarm cards", func(c cards) bool {
		return strings.Contains(c["device-leak"], "LEAK DETECTED") &&
			strings.Contains(c["device-smoke"], "SMOKE DETECTED") &&
			strings.Contains(c["device-door"], ">Open<")
	})
}

func TestSSESkipsDevicesHiddenFromWeb(t *testing.T) {
	hidden := false
	cfg := climateSensors(2)
	cfg[1].Web = &hidden
	ws, _ := newTestWebServer(t, cfg...)

	events := connectSSE(t, ws)
	got := cards{}
	got.await(t, events, "visible card", func(c cards) bool { return len(c) == 1 })
	if _, ok := got[sseEventName(cfg[1].ID)]; ok {
		t.Error("hidden device streamed")
	}
	expectQuiet(t, events)
}
