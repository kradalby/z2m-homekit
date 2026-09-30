package z2mhomekit

import (
	"cmp"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chasefleming/elem-go"
	"github.com/chasefleming/elem-go/attrs"
	"github.com/kradalby/kra/web"
	"tailscale.com/util/eventbus"

	"github.com/kradalby/z2m-homekit/devices"
	"github.com/kradalby/z2m-homekit/events"
)

//go:embed assets/style.css
var cssContent string

// Scripts ship in the binary so live updates work on a LAN without internet.
//
//go:embed assets/vendor
var vendorFS embed.FS

var vendorServer = http.FileServerFS(vendorFS)

type deviceStateProvider interface {
	Snapshot() *devices.Snapshot
}

type DeviceController interface {
	SetPower(ctx context.Context, deviceID string, on bool) error
	SetBrightness(ctx context.Context, deviceID string, brightness int) error
}

// WebServer manages the web UI
type WebServer struct {
	logger           *slog.Logger
	kraweb           *web.KraWeb
	deviceProvider   deviceStateProvider
	controller       DeviceController
	eventLog         []string
	eventLogMu       sync.Mutex
	eventBus         *events.Bus
	client           *eventbus.Client
	statusSubscriber *eventbus.Subscriber[events.ConnectionStatusEvent]
	connectionState  map[string]events.ConnectionStatusEvent
	statusMu         sync.RWMutex
	sseClients       atomic.Int64
	hapPin           string
	qrCode           string
	hapManager       *HAPManager
	ctx              context.Context

	now     func() time.Time
	refresh time.Duration // how often SSE re-renders cards absent state changes
}

// NewWebServer creates a new web server
func NewWebServer(logger *slog.Logger, deviceProvider deviceStateProvider, controller DeviceController, bus *events.Bus, kraweb *web.KraWeb, hapPin, qrCode string, hapManager *HAPManager) *WebServer {
	client, err := bus.Client(events.ClientWeb)
	if err != nil {
		panic(fmt.Sprintf("failed to create web client: %v", err))
	}

	return &WebServer{
		logger:           logger,
		kraweb:           kraweb,
		deviceProvider:   deviceProvider,
		controller:       controller,
		eventLog:         make([]string, 0, 100),
		eventBus:         bus,
		client:           client,
		statusSubscriber: eventbus.Subscribe[events.ConnectionStatusEvent](client),
		connectionState:  make(map[string]events.ConnectionStatusEvent),
		hapPin:           hapPin,
		qrCode:           qrCode,
		hapManager:       hapManager,
		ctx:              context.Background(),
		now:              time.Now,
		refresh:          5 * time.Second,
	}
}

// LogEvent adds an event to the log. Called from concurrent HTTP handlers.
func (ws *WebServer) LogEvent(event string) {
	ws.eventLogMu.Lock()
	defer ws.eventLogMu.Unlock()

	ws.eventLog = append(ws.eventLog, fmt.Sprintf("%s: %s", time.Now().Format("15:04:05"), event))
	if len(ws.eventLog) > 100 {
		ws.eventLog = ws.eventLog[1:]
	}
}

// recentEvents returns up to n log entries, newest first.
func (ws *WebServer) recentEvents(n int) []string {
	ws.eventLogMu.Lock()
	defer ws.eventLogMu.Unlock()

	out := make([]string, 0, min(n, len(ws.eventLog)))
	for i := len(ws.eventLog) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, ws.eventLog[i])
	}

	return out
}

func (ws *WebServer) Start(ctx context.Context) {
	ws.ctx = ctx
	go ws.processConnectionStatuses(ctx)
	ws.publishConnectionStatus(events.ConnectionStatusConnecting, "")

	go func() {
		if ws.kraweb == nil {
			return
		}
		ws.logger.Info("Starting web interface")
		ws.publishConnectionStatus(events.ConnectionStatusConnected, "")
		if err := ws.kraweb.ListenAndServe(ctx); err != nil {
			ws.logger.Error("Web server error", slog.Any("error", err))
			if errors.Is(err, context.Canceled) {
				ws.publishConnectionStatus(events.ConnectionStatusDisconnected, "")
			} else {
				ws.publishConnectionStatus(events.ConnectionStatusFailed, err.Error())
			}
			return
		}
		ws.publishConnectionStatus(events.ConnectionStatusDisconnected, "")
	}()
}

func (ws *WebServer) Close() {
	ws.statusSubscriber.Close()
}

func (ws *WebServer) publishConnectionStatus(status events.ConnectionStatus, errMsg string) {
	if ws.eventBus == nil || ws.client == nil {
		return
	}

	ws.eventBus.PublishConnectionStatus(ws.client, events.ConnectionStatusEvent{
		Timestamp: time.Now(),
		Component: "web",
		Status:    status,
		Error:     errMsg,
	})
}

func (ws *WebServer) processConnectionStatuses(ctx context.Context) {
	for {
		select {
		case event := <-ws.statusSubscriber.Events():
			ws.statusMu.Lock()
			ws.connectionState[event.Component] = event
			ws.statusMu.Unlock()
		case <-ctx.Done():
			return
		}
	}
}

func (ws *WebServer) snapshotStatuses() []events.ConnectionStatusEvent {
	ws.statusMu.RLock()
	defer ws.statusMu.RUnlock()

	statuses := make([]events.ConnectionStatusEvent, 0, len(ws.connectionState))
	for _, evt := range ws.connectionState {
		statuses = append(statuses, evt)
	}

	slices.SortFunc(statuses, func(a, b events.ConnectionStatusEvent) int {
		return cmp.Compare(a.Component, b.Component)
	})

	return statuses
}

func (ws *WebServer) renderPage(title string, content elem.Node) string {
	page := elem.Html(attrs.Props{},
		elem.Head(attrs.Props{},
			elem.Meta(attrs.Props{attrs.Charset: "utf-8"}),
			elem.Meta(attrs.Props{attrs.Name: "viewport", attrs.Content: "width=device-width, initial-scale=1"}),
			elem.Title(attrs.Props{}, elem.Text(title)),
			elem.Script(attrs.Props{attrs.Src: "/assets/vendor/htmx.min.js"}),
			elem.Script(attrs.Props{attrs.Src: "/assets/vendor/htmx-ext-sse.js"}),
			elem.Style(attrs.Props{}, elem.Text(cssContent)),
		),
		elem.Body(attrs.Props{}, content),
	)
	return page.Render()
}

// renderDeviceCard is the only card renderer: the page, htmx responses and
// the SSE stream all use it.
func (ws *WebServer) renderDeviceCard(deviceID string, info devices.Device, state devices.State, now time.Time) elem.Node {
	statusClass := "sensor"
	icon := ws.getDeviceIcon(info.Type)

	cardChildren := []elem.Node{
		elem.Div(attrs.Props{attrs.Class: "device-header"},
			elem.Div(attrs.Props{attrs.Class: "device-icon"}, elem.Text(icon)),
			elem.Div(attrs.Props{attrs.Class: "device-info"},
				elem.Div(attrs.Props{attrs.Class: "device-name"}, elem.Text(info.Name)),
				ws.renderConnectionStatus(state, now),
			),
		),
	}

	switch info.Type {
	case devices.DeviceTypeClimateSensor:
		cardChildren = append(cardChildren, ws.renderClimateSensor(info, state))
	case devices.DeviceTypeOccupancySensor:
		cardChildren = append(cardChildren, ws.renderOccupancySensor(info, state))
	case devices.DeviceTypeContactSensor:
		cardChildren = append(cardChildren, ws.renderContactSensor(info, state))
	case devices.DeviceTypeLeakSensor:
		cardChildren = append(cardChildren, ws.renderLeakSensor(info, state))
	case devices.DeviceTypeSmokeSensor:
		cardChildren = append(cardChildren, ws.renderSmokeSensor(info, state))
	case devices.DeviceTypeLightbulb:
		statusClass, cardChildren = ws.renderLightbulb(deviceID, info, state, now, cardChildren)
	case devices.DeviceTypeOutlet, devices.DeviceTypeSwitch:
		statusClass, cardChildren = ws.renderOutlet(deviceID, info, state, now, cardChildren)
	case devices.DeviceTypeFan:
		statusClass, cardChildren = ws.renderFan(deviceID, info, state, now, cardChildren)
	}

	return elem.Div(
		attrs.Props{
			attrs.ID:         "device-" + deviceID,
			attrs.Class:      "device " + statusClass,
			"data-device-id": deviceID,
			"sse-swap":       sseEventName(deviceID),
			"hx-swap":        "outerHTML",
		},
		cardChildren...,
	)
}

func (ws *WebServer) getDeviceIcon(deviceType devices.DeviceType) string {
	switch deviceType {
	case devices.DeviceTypeClimateSensor:
		return "🌡️"
	case devices.DeviceTypeOccupancySensor:
		return "👤"
	case devices.DeviceTypeContactSensor:
		return "🚪"
	case devices.DeviceTypeLeakSensor:
		return "💧"
	case devices.DeviceTypeSmokeSensor:
		return "🔥"
	case devices.DeviceTypeLightbulb:
		return "💡"
	case devices.DeviceTypeOutlet:
		return "🔌"
	case devices.DeviceTypeSwitch:
		return "🔘"
	case devices.DeviceTypeFan:
		return "🌀"
	default:
		return "📱"
	}
}

func (ws *WebServer) renderClimateSensor(info devices.Device, state devices.State) elem.Node {
	var items []elem.Node

	if info.Features.Temperature && state.Temperature.IsSet() {
		items = append(items,
			elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
				elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Temperature:")),
				elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "temperature-value"},
					elem.Text(fmt.Sprintf("%.1f °C", state.Temperature.Get())),
				),
			),
		)
	}

	if info.Features.Humidity && state.Humidity.IsSet() {
		items = append(items,
			elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
				elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Humidity:")),
				elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "humidity-value"},
					elem.Text(fmt.Sprintf("%.1f %%", state.Humidity.Get())),
				),
			),
		)
	}

	if info.Features.Battery && state.Battery.IsSet() {
		items = append(items,
			elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
				elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Battery:")),
				elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "battery-value"},
					elem.Text(fmt.Sprintf("%d %%", state.Battery.Get())),
				),
			),
		)
	}

	if info.Features.Pressure && state.Pressure.IsSet() {
		items = append(items,
			elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
				elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Pressure:")),
				elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "pressure-value"},
					elem.Text(fmt.Sprintf("%.1f hPa", state.Pressure.Get())),
				),
			),
		)
	}

	return elem.Div(attrs.Props{attrs.Class: "sensor-values"}, items...)
}

func (ws *WebServer) renderOccupancySensor(info devices.Device, state devices.State) elem.Node {
	var items []elem.Node

	occupancyText := "Unknown"
	if state.Occupancy.IsSet() {
		if state.Occupancy.Get() {
			occupancyText = "Detected"
		} else {
			occupancyText = "Clear"
		}
	}

	items = append(items,
		elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
			elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Occupancy:")),
			elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "occupancy-value"},
				elem.Text(occupancyText),
			),
		),
	)

	if info.Features.Battery && state.Battery.IsSet() {
		items = append(items,
			elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
				elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Battery:")),
				elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "battery-value"},
					elem.Text(fmt.Sprintf("%d %%", state.Battery.Get())),
				),
			),
		)
	}

	if info.Features.Illuminance && state.Illuminance.IsSet() {
		items = append(items,
			elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
				elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Illuminance:")),
				elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "illuminance-value"},
					elem.Text(fmt.Sprintf("%d lux", state.Illuminance.Get())),
				),
			),
		)
	}

	return elem.Div(attrs.Props{attrs.Class: "sensor-values"}, items...)
}

func (ws *WebServer) renderContactSensor(info devices.Device, state devices.State) elem.Node {
	var items []elem.Node

	contactText := "Unknown"
	if state.Contact.IsSet() {
		if state.Contact.Get() {
			contactText = "Closed"
		} else {
			contactText = "Open"
		}
	}

	items = append(items,
		elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
			elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Contact:")),
			elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "contact-value"},
				elem.Text(contactText),
			),
		),
	)

	if info.Features.Battery && state.Battery.IsSet() {
		items = append(items,
			elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
				elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Battery:")),
				elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "battery-value"},
					elem.Text(fmt.Sprintf("%d %%", state.Battery.Get())),
				),
			),
		)
	}

	return elem.Div(attrs.Props{attrs.Class: "sensor-values"}, items...)
}

func (ws *WebServer) renderLeakSensor(info devices.Device, state devices.State) elem.Node {
	var items []elem.Node

	leakText := "Unknown"
	if state.WaterLeak.IsSet() {
		if state.WaterLeak.Get() {
			leakText = "LEAK DETECTED"
		} else {
			leakText = "No Leak"
		}
	}

	items = append(items,
		elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
			elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Water Leak:")),
			elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "water-leak-value"},
				elem.Text(leakText),
			),
		),
	)

	if info.Features.Battery && state.Battery.IsSet() {
		items = append(items,
			elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
				elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Battery:")),
				elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "battery-value"},
					elem.Text(fmt.Sprintf("%d %%", state.Battery.Get())),
				),
			),
		)
	}

	return elem.Div(attrs.Props{attrs.Class: "sensor-values"}, items...)
}

func (ws *WebServer) renderSmokeSensor(info devices.Device, state devices.State) elem.Node {
	var items []elem.Node

	smokeText := "Unknown"
	if state.Smoke.IsSet() {
		if state.Smoke.Get() {
			smokeText = "SMOKE DETECTED"
		} else {
			smokeText = "Clear"
		}
	}

	items = append(items,
		elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
			elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Smoke:")),
			elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "smoke-value"},
				elem.Text(smokeText),
			),
		),
	)

	if info.Features.Battery && state.Battery.IsSet() {
		items = append(items,
			elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
				elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Battery:")),
				elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "battery-value"},
					elem.Text(fmt.Sprintf("%d %%", state.Battery.Get())),
				),
			),
		)
	}

	return elem.Div(attrs.Props{attrs.Class: "sensor-values"}, items...)
}

func (ws *WebServer) renderFan(deviceID string, info devices.Device, state devices.State, now time.Time, cardChildren []elem.Node) (string, []elem.Node) {
	statusClass := "off"
	statusText := "OFF"
	buttonClass := "on"
	buttonText := "Turn On"
	buttonAction := "on"

	if state.On.Get() {
		statusClass = "on"
		statusText = "ON"
		buttonClass = "off"
		buttonText = "Turn Off"
		buttonAction = "off"
	}

	cardChildren[0] = elem.Div(attrs.Props{attrs.Class: "device-header"},
		elem.Div(attrs.Props{attrs.Class: "device-icon"}, elem.Text("🌀")),
		elem.Div(attrs.Props{attrs.Class: "device-info"},
			elem.Div(attrs.Props{attrs.Class: "device-name"}, elem.Text(info.Name)),
			elem.Div(attrs.Props{attrs.Class: "device-status"},
				elem.Div(attrs.Props{"data-role": "status-label"}, elem.Text(fmt.Sprintf("Status: %s", statusText))),
			),
			ws.renderConnectionStatus(state, now),
		),
	)

	// Add fan controls if speed feature is enabled
	if info.Features.Speed && state.FanSpeed.IsSet() {
		cardChildren = append(cardChildren,
			elem.Div(attrs.Props{attrs.Class: "light-controls"},
				elem.Div(attrs.Props{attrs.Class: "light-control-item"},
					elem.Span(attrs.Props{attrs.Class: "light-control-label"}, elem.Text("Speed:")),
					elem.Span(attrs.Props{attrs.Class: "light-control-value", "data-role": "fan-speed-value"},
						elem.Text(fmt.Sprintf("%d%%", state.FanSpeed.Get())),
					),
				),
			),
		)
	}

	cardChildren = append(cardChildren, elem.Form(
		attrs.Props{
			"hx-post":   "/toggle/" + deviceID,
			"hx-target": "#device-" + deviceID,
			"hx-swap":   "outerHTML",
		},
		elem.Input(attrs.Props{attrs.Type: "hidden", attrs.Name: "action", attrs.Value: buttonAction, "data-role": "action-input"}),
		elem.Button(
			attrs.Props{attrs.Type: "submit", attrs.Class: buttonClass, "data-role": "toggle-button"},
			elem.Text(buttonText),
		),
	))

	return statusClass, cardChildren
}

func (ws *WebServer) renderLightbulb(deviceID string, info devices.Device, state devices.State, now time.Time, cardChildren []elem.Node) (string, []elem.Node) {
	statusClass := "off"
	statusText := "OFF"
	buttonClass := "on"
	buttonText := "Turn On"
	buttonAction := "on"

	if state.On.Get() {
		statusClass = "on"
		statusText = "ON"
		buttonClass = "off"
		buttonText = "Turn Off"
		buttonAction = "off"
	}

	// Update status label
	cardChildren[0] = elem.Div(attrs.Props{attrs.Class: "device-header"},
		elem.Div(attrs.Props{attrs.Class: "device-icon"}, elem.Text("💡")),
		elem.Div(attrs.Props{attrs.Class: "device-info"},
			elem.Div(attrs.Props{attrs.Class: "device-name"}, elem.Text(info.Name)),
			elem.Div(attrs.Props{attrs.Class: "device-status"},
				elem.Div(attrs.Props{"data-role": "status-label"}, elem.Text(fmt.Sprintf("Status: %s", statusText))),
			),
			ws.renderConnectionStatus(state, now),
		),
	)

	// Add light controls if applicable
	var lightItems []elem.Node

	if info.Features.Brightness && state.Brightness.IsSet() {
		brightnessHAP := devices.Z2MBrightnessToHAP(state.Brightness.Get())
		lightItems = append(lightItems,
			elem.Div(attrs.Props{attrs.Class: "light-control-item brightness-slider-container"},
				elem.Span(attrs.Props{attrs.Class: "light-control-label"}, elem.Text("Brightness:")),
				elem.Span(attrs.Props{attrs.Class: "light-control-value", "data-role": "brightness-value"},
					elem.Text(fmt.Sprintf("%d%%", brightnessHAP)),
				),
				elem.Input(attrs.Props{
					attrs.Type:       "range",
					attrs.Class:      "brightness-slider",
					attrs.Min:        "0",
					attrs.Max:        "100",
					attrs.Value:      fmt.Sprintf("%d", brightnessHAP),
					attrs.Name:       "brightness",
					"data-device-id": deviceID,
					"data-role":      "brightness-slider",
					"hx-post":        "/brightness/" + deviceID,
					"hx-trigger":     "change",
					"hx-target":      "#device-" + deviceID,
					"hx-swap":        "outerHTML",
					"hx-include":     "this",
				}),
			),
		)
	}

	if info.Features.Color && state.Hue.IsSet() {
		lightItems = append(lightItems,
			elem.Div(attrs.Props{attrs.Class: "light-control-item"},
				elem.Span(attrs.Props{attrs.Class: "light-control-label"}, elem.Text("Hue:")),
				elem.Span(attrs.Props{attrs.Class: "light-control-value", "data-role": "hue-value"},
					elem.Text(fmt.Sprintf("%.0f°", state.Hue.Get())),
				),
			),
		)
	}

	if info.Features.Color && state.Saturation.IsSet() {
		lightItems = append(lightItems,
			elem.Div(attrs.Props{attrs.Class: "light-control-item"},
				elem.Span(attrs.Props{attrs.Class: "light-control-label"}, elem.Text("Saturation:")),
				elem.Span(attrs.Props{attrs.Class: "light-control-value", "data-role": "saturation-value"},
					elem.Text(fmt.Sprintf("%.0f%%", state.Saturation.Get())),
				),
			),
		)
	}

	if info.Features.ColorTemperature && state.ColorTemp.IsSet() {
		lightItems = append(lightItems,
			elem.Div(attrs.Props{attrs.Class: "light-control-item"},
				elem.Span(attrs.Props{attrs.Class: "light-control-label"}, elem.Text("Color Temp:")),
				elem.Span(attrs.Props{attrs.Class: "light-control-value", "data-role": "color-temp-value"},
					elem.Text(fmt.Sprintf("%d mireds", state.ColorTemp.Get())),
				),
			),
		)
	}

	if len(lightItems) > 0 {
		cardChildren = append(cardChildren, elem.Div(attrs.Props{attrs.Class: "light-controls"}, lightItems...))
	}

	// Add toggle button
	cardChildren = append(cardChildren, elem.Form(
		attrs.Props{
			"hx-post":   "/toggle/" + deviceID,
			"hx-target": "#device-" + deviceID,
			"hx-swap":   "outerHTML",
		},
		elem.Input(attrs.Props{attrs.Type: "hidden", attrs.Name: "action", attrs.Value: buttonAction, "data-role": "action-input"}),
		elem.Button(
			attrs.Props{attrs.Type: "submit", attrs.Class: buttonClass, "data-role": "toggle-button"},
			elem.Text(buttonText),
		),
	))

	return statusClass, cardChildren
}

func (ws *WebServer) renderOutlet(deviceID string, info devices.Device, state devices.State, now time.Time, cardChildren []elem.Node) (string, []elem.Node) {
	statusClass := "off"
	statusText := "OFF"
	buttonClass := "on"
	buttonText := "Turn On"
	buttonAction := "on"

	if state.On.Get() {
		statusClass = "on"
		statusText = "ON"
		buttonClass = "off"
		buttonText = "Turn Off"
		buttonAction = "off"
	}

	icon := "🔌"
	if info.Type == devices.DeviceTypeSwitch {
		icon = "🔘"
	}

	cardChildren[0] = elem.Div(attrs.Props{attrs.Class: "device-header"},
		elem.Div(attrs.Props{attrs.Class: "device-icon"}, elem.Text(icon)),
		elem.Div(attrs.Props{attrs.Class: "device-info"},
			elem.Div(attrs.Props{attrs.Class: "device-name"}, elem.Text(info.Name)),
			elem.Div(attrs.Props{attrs.Class: "device-status"},
				elem.Div(attrs.Props{"data-role": "status-label"}, elem.Text(fmt.Sprintf("Status: %s", statusText))),
			),
			ws.renderConnectionStatus(state, now),
		),
	)

	cardChildren = append(cardChildren, elem.Form(
		attrs.Props{
			"hx-post":   "/toggle/" + deviceID,
			"hx-target": "#device-" + deviceID,
			"hx-swap":   "outerHTML",
		},
		elem.Input(attrs.Props{attrs.Type: "hidden", attrs.Name: "action", attrs.Value: buttonAction, "data-role": "action-input"}),
		elem.Button(
			attrs.Props{attrs.Type: "submit", attrs.Class: buttonClass, "data-role": "toggle-button"},
			elem.Text(buttonText),
		),
	))

	return statusClass, cardChildren
}

func (ws *WebServer) renderConnectionStatus(state devices.State, now time.Time) elem.Node {
	indicator, text := connectionStatus(state.LastSeen, now)

	return elem.Div(attrs.Props{attrs.Class: "connection-status"},
		elem.Span(attrs.Props{"data-role": "connection-indicator", attrs.Class: "connection-indicator " + indicator}),
		elem.Span(attrs.Props{"data-role": "connection-text"}, elem.Text(text)),
	)
}

// connectionStatus derives a device's link state from when it was last heard.
// The text is absolute so a card only changes when the device reports or
// crosses a threshold, not every second.
func connectionStatus(lastSeen, now time.Time) (indicator, text string) {
	if lastSeen.IsZero() {
		return "disconnected", "Never seen"
	}

	since := now.Sub(lastSeen)
	text = "Last seen " + lastSeen.Format(time.TimeOnly)
	if since >= 24*time.Hour {
		text = "Last seen " + lastSeen.Format(time.DateTime)
	}
	switch {
	case since < 30*time.Second:
		return "connected", text
	case since < 60*time.Second:
		return "stale", text
	default:
		return "disconnected", text
	}
}

// HandleAssets serves the vendored scripts under /assets/vendor/.
func (ws *WebServer) HandleAssets(w http.ResponseWriter, r *http.Request) {
	vendorServer.ServeHTTP(w, r)
}

// HandleIndex renders the main dashboard
func (ws *WebServer) HandleIndex(w http.ResponseWriter, r *http.Request) {
	var deviceElements []elem.Node

	snapshot := ws.deviceProvider.Snapshot()
	for _, ds := range snapshot.All() {
		if !ds.Device.Web {
			continue
		}
		deviceElements = append(deviceElements, ws.renderDeviceCard(ds.Device.ID, ds.Device, ds.State, ws.now()))
	}

	var eventElements []elem.Node
	for _, entry := range ws.recentEvents(20) {
		eventElements = append(eventElements, elem.Div(attrs.Props{attrs.Class: "event"}, elem.Text(entry)))
	}

	var homekitSection elem.Node
	if ws.hapPin != "" {
		var qrContent []elem.Node
		qrContent = append(qrContent,
			elem.Div(attrs.Props{attrs.Class: "homekit-pin"},
				elem.Span(attrs.Props{attrs.Class: "homekit-pin-label"}, elem.Text("Setup PIN")),
				elem.Span(attrs.Props{attrs.Class: "homekit-pin-value"}, elem.Text(ws.hapPin)),
			),
		)

		if ws.qrCode != "" {
			qrContent = append(qrContent,
				elem.Div(attrs.Props{attrs.Class: "qr-code-block"},
					elem.Pre(attrs.Props{attrs.Class: "qr-code"}, elem.Text(ws.qrCode)),
				),
				elem.P(attrs.Props{attrs.Class: "homekit-instructions"},
					elem.Text("Scan the QR code from the Home app or camera on your iPhone/iPad."),
				),
			)
		} else {
			qrContent = append(qrContent,
				elem.P(attrs.Props{attrs.Class: "homekit-instructions"},
					elem.Text("QR code is not available on this host. Use the PIN above in the Home app."),
				),
			)
		}

		qrContent = append(qrContent,
			elem.P(attrs.Props{attrs.Class: "homekit-instructions"},
				elem.Text("Home app -> Add Accessory -> More Options -> Select \"z2m-homekit Bridge\"."),
			),
			elem.A(attrs.Props{attrs.Href: "/qrcode", attrs.Class: "homekit-link"}, elem.Text("Open standalone QR view")),
		)

		homekitSection = elem.Details(attrs.Props{attrs.Class: "homekit-banner"},
			elem.Summary(nil,
				elem.Span(attrs.Props{attrs.Class: "homekit-summary-title"}, elem.Text("HomeKit Pairing")),
				elem.Span(attrs.Props{attrs.Class: "homekit-summary-caption"}, elem.Text("Tap to reveal setup PIN & QR code")),
			),
			elem.Div(attrs.Props{attrs.Class: "homekit-banner-content"}, qrContent...),
		)
	}

	content := elem.Div(attrs.Props{},
		elem.H1(attrs.Props{}, elem.Text("Zigbee2MQTT HomeKit Bridge")),
		elem.P(attrs.Props{}, elem.Text(fmt.Sprintf("Managing %d devices", snapshot.Len()))),
		homekitSection,
		elem.Div(attrs.Props{attrs.Class: "devices-grid", "hx-ext": "sse", "sse-connect": "/events"}, deviceElements...),
		elem.Div(attrs.Props{attrs.Class: "events"},
			elem.H2(attrs.Props{}, elem.Text("Recent Events")),
			elem.Div(attrs.Props{}, eventElements...),
		),
	)

	w.Header().Set("Content-Type", "text/html")
	if _, err := fmt.Fprint(w, ws.renderPage("z2m-homekit", content)); err != nil {
		ws.logger.Error("Failed to write response", slog.Any("error", err))
	}
}

// HandleToggle handles device toggle requests
func (ws *WebServer) HandleToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/toggle/")
	deviceID := path

	ds, exists := ws.deviceProvider.Snapshot().Get(deviceID)
	if !exists {
		http.Error(w, "Device not found", http.StatusNotFound)
		return
	}

	if !ds.Device.Web {
		http.Error(w, "Device not available on web", http.StatusNotFound)
		return
	}

	action := r.FormValue("action")
	on := action == "on"

	if err := ws.controller.SetPower(r.Context(), deviceID, on); err != nil {
		ws.logger.Error("Failed to set power", "device_id", deviceID, "error", err)
		http.Error(w, "Failed to set power", http.StatusInternalServerError)
		return
	}

	ws.LogEvent(fmt.Sprintf("Web UI: Toggle %s -> %v", deviceID, on))

	if r.Header.Get("HX-Request") == "true" {
		ds, _ = ws.deviceProvider.Snapshot().Get(deviceID)

		w.Header().Set("Content-Type", "text/html")
		if _, err := fmt.Fprint(w, ws.renderDeviceCard(deviceID, ds.Device, ds.State, ws.now()).Render()); err != nil {
			ws.logger.Error("Failed to write response", slog.Any("error", err))
		}
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// HandleBrightness handles brightness slider requests
func (ws *WebServer) HandleBrightness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/brightness/")
	deviceID := path

	ds, exists := ws.deviceProvider.Snapshot().Get(deviceID)
	if !exists {
		http.Error(w, "Device not found", http.StatusNotFound)
		return
	}

	if !ds.Device.Web {
		http.Error(w, "Device not available on web", http.StatusNotFound)
		return
	}

	brightnessStr := r.FormValue("brightness")
	var brightness int
	if _, err := fmt.Sscanf(brightnessStr, "%d", &brightness); err != nil {
		http.Error(w, "Invalid brightness value", http.StatusBadRequest)
		return
	}

	// Clamp brightness to valid range
	brightness = min(max(brightness, 0), 100)

	if err := ws.controller.SetBrightness(r.Context(), deviceID, brightness); err != nil {
		ws.logger.Error("Failed to set brightness", "device_id", deviceID, "error", err)
		http.Error(w, "Failed to set brightness", http.StatusInternalServerError)
		return
	}

	ws.LogEvent(fmt.Sprintf("Web UI: Brightness %s -> %d%%", deviceID, brightness))

	if r.Header.Get("HX-Request") == "true" {
		ds, _ = ws.deviceProvider.Snapshot().Get(deviceID)

		w.Header().Set("Content-Type", "text/html")
		if _, err := fmt.Fprint(w, ws.renderDeviceCard(deviceID, ds.Device, ds.State, ws.now()).Render()); err != nil {
			ws.logger.Error("Failed to write response", slog.Any("error", err))
		}
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// HandleEventBusDebug renders a simple diagnostic view of device and component state.
func (ws *WebServer) HandleEventBusDebug(w http.ResponseWriter, r *http.Request) {
	rows := []elem.Node{
		elem.Tr(attrs.Props{},
			elem.Th(attrs.Props{}, elem.Text("Device ID")),
			elem.Th(attrs.Props{}, elem.Text("Name")),
			elem.Th(attrs.Props{}, elem.Text("On")),
			elem.Th(attrs.Props{}, elem.Text("Last Seen")),
			elem.Th(attrs.Props{}, elem.Text("Connection")),
		),
	}

	for _, ds := range ws.deviceProvider.Snapshot().All() {
		onText := "n/a"
		if on, ok := ds.State.On.GetOk(); ok {
			onText = fmt.Sprintf("%t", on)
		}
		connection, _ := connectionStatus(ds.State.LastSeen, ws.now())
		rows = append(rows,
			elem.Tr(attrs.Props{},
				elem.Td(attrs.Props{}, elem.Text(ds.Device.ID)),
				elem.Td(attrs.Props{}, elem.Text(ds.Device.Name)),
				elem.Td(attrs.Props{}, elem.Text(onText)),
				elem.Td(attrs.Props{}, elem.Text(ds.State.LastSeen.Format(time.RFC3339))),
				elem.Td(attrs.Props{}, elem.Text(connection)),
			),
		)
	}

	statusRows := []elem.Node{
		elem.Tr(attrs.Props{},
			elem.Th(attrs.Props{}, elem.Text("Component")),
			elem.Th(attrs.Props{}, elem.Text("Status")),
			elem.Th(attrs.Props{}, elem.Text("Updated")),
			elem.Th(attrs.Props{}, elem.Text("Error")),
		),
	}

	for _, status := range ws.snapshotStatuses() {
		statusRows = append(statusRows,
			elem.Tr(attrs.Props{},
				elem.Td(attrs.Props{}, elem.Text(status.Component)),
				elem.Td(attrs.Props{}, elem.Text(string(status.Status))),
				elem.Td(attrs.Props{}, elem.Text(status.Timestamp.Format(time.RFC3339))),
				elem.Td(attrs.Props{}, elem.Text(status.Error)),
			),
		)
	}

	content := elem.Div(attrs.Props{},
		elem.H1(attrs.Props{}, elem.Text("EventBus Debug")),
		elem.P(attrs.Props{}, elem.Text(fmt.Sprintf("Connected SSE clients: %d", ws.sseClients.Load()))),
		elem.Table(attrs.Props{"border": "1", "cellpadding": "4", "cellspacing": "0"}, rows...),
		elem.H2(attrs.Props{}, elem.Text("Component Status")),
		elem.Table(attrs.Props{"border": "1", "cellpadding": "4", "cellspacing": "0"}, statusRows...),
	)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := fmt.Fprint(w, ws.renderPage("EventBus Debug", content)); err != nil {
		ws.logger.Error("Failed to write eventbus debug response", slog.Any("error", err))
	}
}

// HandleSSE streams each web-visible device's rendered card whenever it
// changes. Every wake renders from the latest snapshot, so a slow client
// skips intermediate states rather than losing the newest one, and it can
// never see an older state after a newer one.
func (ws *WebServer) HandleSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ws.sseClients.Add(1)
	defer ws.sseClients.Add(-1)

	// Connection state ages with the clock, not with reports, so re-render
	// periodically even when nothing is published.
	refresh := time.NewTicker(ws.refresh)
	defer refresh.Stop()

	sent := make(map[string]string) // device ID -> card last sent to this client
	for {
		snap := ws.deviceProvider.Snapshot()
		now := ws.now()
		for _, ds := range snap.All() {
			if !ds.Device.Web {
				continue
			}

			card := ws.renderDeviceCard(ds.Device.ID, ds.Device, ds.State, now).Render()
			if sent[ds.Device.ID] == card {
				continue
			}
			if err := writeSSE(w, sseEventName(ds.Device.ID), card); err != nil {
				return
			}
			sent[ds.Device.ID] = card
		}
		flusher.Flush()

		select {
		case <-snap.Changed():
		case <-refresh.C:
		case <-r.Context().Done():
			return
		case <-ws.ctx.Done():
			return
		}
	}
}

func sseEventName(deviceID string) string {
	return "device-" + deviceID
}

// writeSSE frames data as one server-sent event; every line of a multi-line
// payload needs its own data: prefix.
func writeSSE(w io.Writer, event, data string) error {
	var b strings.Builder
	b.WriteString("event: " + event + "\n")
	for line := range strings.SplitSeq(data, "\n") {
		b.WriteString("data: " + line + "\n")
	}
	b.WriteString("\n")

	_, err := io.WriteString(w, b.String())

	return err
}

// HandleHealth exposes a JSON health summary.
func (ws *WebServer) HandleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	snapshot := ws.deviceProvider.Snapshot()

	resp := struct {
		Status     string    `json:"status"`
		Devices    int       `json:"devices"`
		SSEClients int       `json:"sse_clients"`
		Timestamp  time.Time `json:"timestamp"`
	}{
		Status:     "ok",
		Devices:    snapshot.Len(),
		SSEClients: int(ws.sseClients.Load()),
		Timestamp:  time.Now(),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		ws.logger.Error("Failed to write health response", slog.Any("error", err))
	}
}

// HandleQRCode renders the current HomeKit QR code for terminal access.
func (ws *WebServer) HandleQRCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if ws.qrCode == "" {
		if _, err := fmt.Fprintf(w, "HomeKit PIN: %s\nQR code is not available on this host.\n", ws.hapPin); err != nil {
			ws.logger.Error("failed to render QR fallback", slog.Any("error", err))
		}
		return
	}

	if _, err := fmt.Fprintf(w, "HomeKit PIN: %s\n\n%s\n", ws.hapPin, ws.qrCode); err != nil {
		ws.logger.Error("failed to render QR code", slog.Any("error", err))
	}
}
