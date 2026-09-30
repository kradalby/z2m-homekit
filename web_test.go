package z2mhomekit

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
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
			Features: devices.DeviceFeatures{Temperature: true}, Web: true,
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
		devices.Device{ID: "leak", Name: "Leak", Topic: "leak", Type: devices.DeviceTypeLeakSensor, Web: true},
		devices.Device{ID: "smoke", Name: "Smoke", Topic: "smoke", Type: devices.DeviceTypeSmokeSensor, Web: true},
		devices.Device{ID: "door", Name: "Door", Topic: "door", Type: devices.DeviceTypeContactSensor, Web: true},
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
	cfg := climateSensors(2)
	cfg[1].Web = false
	ws, _ := newTestWebServer(t, cfg...)

	events := connectSSE(t, ws)
	got := cards{}
	got.await(t, events, "visible card", func(c cards) bool { return len(c) == 1 })
	if _, ok := got[sseEventName(cfg[1].ID)]; ok {
		t.Error("hidden device streamed")
	}
	expectQuiet(t, events)
}

// A device that goes quiet must age to stale and then disconnected on an open
// dashboard, rather than showing the status computed when it last reported.
func TestSSEConnectionStatusAgesWithoutReports(t *testing.T) {
	cfg := climateSensors(1)
	ws, dm := newTestWebServer(t, cfg...)
	id := cfg[0].ID

	var mu sync.Mutex
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	now := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	}
	advance := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		clock = clock.Add(d)
	}
	ws.now = now
	ws.refresh = time.Millisecond

	var r devices.Reading
	r.Temperature.Set(20)
	dm.Update(id, r, now())

	events := connectSSE(t, ws)
	got := cards{}
	indicator := func(state string) func(cards) bool {
		return func(c cards) bool {
			return strings.Contains(c[sseEventName(id)], `class="connection-indicator `+state+`"`)
		}
	}

	got.await(t, events, "connected", indicator("connected"))
	advance(45 * time.Second)
	got.await(t, events, "stale", indicator("stale"))
	advance(30 * time.Second)
	got.await(t, events, "disconnected", indicator("disconnected"))

	if card := got[sseEventName(id)]; !strings.Contains(card, "Last seen 03:04:05") {
		t.Errorf("card = %s, want absolute last-seen time", card)
	}

	// Ticks without a threshold crossing must not resend the card.
	expectQuiet(t, events)
}

// The dashboard runs on LANs without internet, so every script it loads,
// including the SSE extension behind live updates, must come from the binary.
func TestPageLoadsOnlyLocalScripts(t *testing.T) {
	ws, _ := newTestWebServer(t, climateSensors(1)...)
	ws.hapPin = "00102003" // an empty pin renders a nil node; config forbids it

	rec := httptest.NewRecorder()
	ws.HandleIndex(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	srcs := regexp.MustCompile(`<script[^>]*\ssrc="([^"]*)"`).FindAllStringSubmatch(rec.Body.String(), -1)
	if len(srcs) == 0 {
		t.Fatal("page loads no scripts")
	}
	for _, m := range srcs {
		src := m[1]
		if u, err := url.Parse(src); err != nil || u.Scheme != "" || u.Host != "" || !strings.HasPrefix(u.Path, "/") {
			t.Errorf("script %q is not served by this host", src)
			continue
		}

		rec := httptest.NewRecorder()
		ws.HandleAssets(rec, httptest.NewRequest(http.MethodGet, src, nil))
		if ct := rec.Header().Get("Content-Type"); rec.Code != http.StatusOK || !strings.Contains(ct, "javascript") || rec.Body.Len() == 0 {
			t.Errorf("GET %s = %d %q, %d bytes; want non-empty javascript", src, rec.Code, ct, rec.Body.Len())
		}
	}
}

var (
	startTagRE = regexp.MustCompile(`<[a-zA-Z][^>]*>`)
	attrRE     = regexp.MustCompile(`([\w-]+)="([^"]*)"`)
)

// startTags returns the attributes of every start tag in s.
func startTags(s string) []map[string]string {
	var tags []map[string]string
	for _, tag := range startTagRE.FindAllString(s, -1) {
		attrs := map[string]string{}
		for _, m := range attrRE.FindAllStringSubmatch(tag, -1) {
			attrs[m[1]] = m[2]
		}
		tags = append(tags, attrs)
	}

	return tags
}

func controlDevices() []devices.Device {
	return []devices.Device{
		{
			ID: "lamp", Name: "Lamp", Topic: "lamp", Type: devices.DeviceTypeLightbulb,
			Features: devices.DeviceFeatures{Brightness: true}, Web: true,
		},
		{ID: "plug", Name: "Plug", Topic: "plug", Type: devices.DeviceTypeOutlet, Web: true},
		{ID: "fan", Name: "Fan", Topic: "fan", Type: devices.DeviceTypeFan, Web: true},
	}
}

// pageAndCards returns the dashboard and every card the SSE stream sends.
func pageAndCards(t *testing.T, cfg []devices.Device) (*WebServer, string, cards) {
	t.Helper()

	ws, dm := newTestWebServer(t, cfg...)
	ws.hapPin = "00102003"
	report(t, dm, "lamp", func(r *devices.Reading) {
		r.On.Set(true)
		r.Brightness.Set(200)
	})

	rec := httptest.NewRecorder()
	ws.HandleIndex(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	events := connectSSE(t, ws)
	got := cards{}
	got.await(t, events, "every card", func(c cards) bool { return len(c) == len(cfg) })

	return ws, rec.Body.String(), got
}

// htmx registers an sse-swap listener only when it processes the element, a
// settle delay after swapping it in, so replacing a listening element drops
// the events that arrive in between. Listeners must sit on slots that SSE
// fills rather than replaces.
func TestSwapsKeepSSEListeners(t *testing.T) {
	cfg := controlDevices()
	_, page, got := pageAndCards(t, cfg)

	slots := 0
	for _, a := range startTags(page) {
		if ev, ok := a["sse-swap"]; ok {
			slots++
			if a["hx-swap"] != "innerHTML" || a["id"] != ev {
				t.Errorf("listener %v: want id %q and hx-swap innerHTML", a, ev)
			}
		}
	}
	if slots != len(cfg) {
		t.Fatalf("page has %d listener slots, want %d", slots, len(cfg))
	}

	for name, card := range got {
		for _, a := range startTags(card) {
			if _, ok := a["sse-swap"]; ok {
				t.Errorf("%s: swapped-in card carries a listener: %v", name, a)
			}
		}
	}
}

// A command response rendered before the device confirms can reach the
// browser after a newer SSE card; swapped into the card, it would restore the
// older state, which SSE then has no reason to resend. Controls must target
// something outside every card, leaving cards to SSE alone.
func TestControlsNeverSwapCards(t *testing.T) {
	cfg := controlDevices()
	_, page, got := pageAndCards(t, cfg)

	onPage, inCards := map[string]bool{}, map[string]bool{}
	for _, a := range startTags(page) {
		if id, ok := a["id"]; ok {
			onPage[id] = true
		}
		if _, ok := a["sse-swap"]; ok {
			inCards[a["id"]] = true // the slot around a card
		}
	}
	for _, card := range got {
		for _, a := range startTags(card) {
			if id, ok := a["id"]; ok {
				inCards[id] = true
			}
		}
	}

	controls := 0
	for name, card := range got {
		for _, a := range startTags(card) {
			if _, ok := a["hx-post"]; !ok {
				continue
			}
			controls++
			// No target means the control itself, which is inside the card.
			id, ok := strings.CutPrefix(a["hx-target"], "#")
			if !ok || !onPage[id] || inCards[id] {
				t.Errorf("%s: control %v targets %q, want an element outside every card", name, a["hx-post"], a["hx-target"])
			}
		}
	}
	if controls < len(cfg) {
		t.Errorf("found %d controls, want at least one per device", controls)
	}
}

type fakeController struct{ err error }

func (f fakeController) SetPower(context.Context, string, bool) error     { return f.err }
func (f fakeController) SetBrightness(context.Context, string, int) error { return f.err }

func commandRequests() map[string]*http.Request {
	post := func(path string, form url.Values) *http.Request {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("HX-Request", "true")
		return r
	}

	return map[string]*http.Request{
		"power":      post("/toggle/lamp", url.Values{"action": {"on"}}),
		"brightness": post("/brightness/lamp", url.Values{"brightness": {"50"}}),
	}
}

func serveCommand(ws *WebServer, name string, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	if name == "power" {
		ws.HandleToggle(rec, r)
	} else {
		ws.HandleBrightness(rec, r)
	}

	return rec
}

// SSE delivers the resulting card once the device reports; see
// TestControlsNeverSwapCards.
func TestCommandSuccessSwapsNothing(t *testing.T) {
	ws, _ := newTestWebServer(t, controlDevices()...)
	ws.controller = fakeController{}

	for name, r := range commandRequests() {
		rec := serveCommand(ws, name, r)
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Errorf("%s: got %d with %q, want 204 and no body", name, rec.Code, rec.Body.String())
		}
	}
}

// With cards left to SSE, a failed command changes no card, so the page must
// show the error itself.
func TestCommandErrorsReachThePage(t *testing.T) {
	ws, page, _ := pageAndCards(t, controlDevices())
	ws.controller = fakeController{err: errors.New("broker down")}

	for name, r := range commandRequests() {
		rec := serveCommand(ws, name, r)
		if rec.Code < 500 || !strings.Contains(rec.Body.String(), "Lamp: failed to set "+name) {
			t.Errorf("%s: got %d %q, want 5xx naming the device", name, rec.Code, rec.Body.String())
		}
	}

	// htmx drops error bodies unless configured to swap them.
	m := regexp.MustCompile(`<meta content='([^']*)' name="htmx-config"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("page has no htmx-config")
	}
	var cfg struct {
		ResponseHandling []struct {
			Code string
			Swap bool
		}
	}
	if err := json.Unmarshal([]byte(m[1]), &cfg); err != nil {
		t.Fatalf("htmx-config: %v", err)
	}
	swaps := false
	for _, rule := range cfg.ResponseHandling {
		if regexp.MustCompile("^" + rule.Code + "$").MatchString("500") {
			swaps = rule.Swap
			break
		}
	}
	if !swaps {
		t.Errorf("htmx-config %s does not swap a 500", m[1])
	}
}
