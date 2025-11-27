package z2mhomekit

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chasefleming/elem-go"
	"github.com/chasefleming/elem-go/attrs"
	"github.com/kradalby/kra/web"
	"github.com/kradalby/z2m-homekit/devices"
	"github.com/kradalby/z2m-homekit/events"
	"tailscale.com/util/eventbus"
)

//go:embed assets/style.css
var cssContent string

//go:embed assets/script.js
var jsContent string

type deviceStateProvider interface {
	Snapshot() map[string]struct {
		Device devices.Device
		State  devices.State
	}
	Device(string) (devices.Device, devices.State, bool)
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
	eventBus         *events.Bus
	client           *eventbus.Client
	stateSubscriber  *eventbus.Subscriber[events.StateUpdateEvent]
	statusSubscriber *eventbus.Subscriber[events.ConnectionStatusEvent]
	currentState     map[string]events.StateUpdateEvent
	connectionState  map[string]events.ConnectionStatusEvent
	stateMu          sync.RWMutex
	statusMu         sync.RWMutex
	sseClients       map[chan events.StateUpdateEvent]struct{}
	sseClientsMu     sync.RWMutex
	hapPin           string
	qrCode           string
	hapManager       *HAPManager
	ctx              context.Context
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
		stateSubscriber:  eventbus.Subscribe[events.StateUpdateEvent](client),
		statusSubscriber: eventbus.Subscribe[events.ConnectionStatusEvent](client),
		currentState:     make(map[string]events.StateUpdateEvent),
		connectionState:  make(map[string]events.ConnectionStatusEvent),
		sseClients:       make(map[chan events.StateUpdateEvent]struct{}),
		hapPin:           hapPin,
		qrCode:           qrCode,
		hapManager:       hapManager,
		ctx:              context.Background(),
	}
}

// LogEvent adds an event to the log
func (ws *WebServer) LogEvent(event string) {
	ws.eventLog = append(ws.eventLog, fmt.Sprintf("%s: %s", time.Now().Format("15:04:05"), event))
	if len(ws.eventLog) > 100 {
		ws.eventLog = ws.eventLog[1:]
	}
}

func (ws *WebServer) Start(ctx context.Context) {
	ws.ctx = ctx
	go ws.processStateChanges(ctx)
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
	ws.stateSubscriber.Close()
	ws.statusSubscriber.Close()

	ws.sseClientsMu.Lock()
	for client := range ws.sseClients {
		close(client)
	}
	ws.sseClients = make(map[chan events.StateUpdateEvent]struct{})
	ws.sseClientsMu.Unlock()
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

func (ws *WebServer) processStateChanges(ctx context.Context) {
	for {
		select {
		case event := <-ws.stateSubscriber.Events():
			ws.stateMu.Lock()
			ws.currentState[event.DeviceID] = event
			ws.stateMu.Unlock()

			ws.logger.Debug("Web UI: State change received", "device_id", event.DeviceID)
			ws.broadcastSSE(event)
		case <-ctx.Done():
			return
		}
	}
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

func (ws *WebServer) broadcastSSE(event events.StateUpdateEvent) {
	ws.sseClientsMu.RLock()
	defer ws.sseClientsMu.RUnlock()

	for client := range ws.sseClients {
		select {
		case client <- event:
		default:
		}
	}
}

func (ws *WebServer) snapshotState() []events.StateUpdateEvent {
	ws.stateMu.RLock()
	defer ws.stateMu.RUnlock()

	snapshot := make([]events.StateUpdateEvent, 0, len(ws.currentState))
	for _, evt := range ws.currentState {
		snapshot = append(snapshot, evt)
	}

	sort.Slice(snapshot, func(i, j int) bool {
		return snapshot[i].DeviceID < snapshot[j].DeviceID
	})

	return snapshot
}

func (ws *WebServer) snapshotStatuses() []events.ConnectionStatusEvent {
	ws.statusMu.RLock()
	defer ws.statusMu.RUnlock()

	statuses := make([]events.ConnectionStatusEvent, 0, len(ws.connectionState))
	for _, evt := range ws.connectionState {
		statuses = append(statuses, evt)
	}

	sort.Slice(statuses, func(i, j int) bool {
		return statuses[i].Component < statuses[j].Component
	})

	return statuses
}

func (ws *WebServer) renderPage(title string, content elem.Node) string {
	page := elem.Html(attrs.Props{},
		elem.Head(attrs.Props{},
			elem.Meta(attrs.Props{attrs.Charset: "utf-8"}),
			elem.Meta(attrs.Props{attrs.Name: "viewport", attrs.Content: "width=device-width, initial-scale=1"}),
			elem.Title(attrs.Props{}, elem.Text(title)),
			elem.Script(attrs.Props{
				attrs.Src: "https://unpkg.com/htmx.org@2.0.4",
			}),
			elem.Style(attrs.Props{}, elem.Text(cssContent)),
			elem.Script(attrs.Props{}, elem.Raw(jsContent)),
		),
		elem.Body(attrs.Props{}, content),
	)
	return page.Render()
}

func (ws *WebServer) renderDeviceCard(deviceID string, info devices.Device, state devices.State) elem.Node {
	statusClass := "sensor"
	icon := ws.getDeviceIcon(info.Type)

	var connectionIndicator, connectionText string
	if state.LastSeen.IsZero() {
		connectionIndicator = "disconnected"
		connectionText = "Never seen"
	} else {
		timeSinceSeen := time.Since(state.LastSeen)
		if timeSinceSeen < 30*time.Second {
			connectionIndicator = "connected"
			connectionText = fmt.Sprintf("Last seen: %s ago", timeSinceSeen.Round(time.Second))
		} else if timeSinceSeen < 60*time.Second {
			connectionIndicator = "stale"
			connectionText = fmt.Sprintf("Last seen: %s ago", timeSinceSeen.Round(time.Second))
		} else {
			connectionIndicator = "disconnected"
			connectionText = fmt.Sprintf("Last seen: %s ago", timeSinceSeen.Round(time.Second))
		}
	}

	cardChildren := []elem.Node{
		elem.Div(attrs.Props{attrs.Class: "device-header"},
			elem.Div(attrs.Props{attrs.Class: "device-icon"}, elem.Text(icon)),
			elem.Div(attrs.Props{attrs.Class: "device-info"},
				elem.Div(attrs.Props{attrs.Class: "device-name"}, elem.Text(info.Name)),
				elem.Div(attrs.Props{attrs.Class: "device-status"},
					elem.Div(attrs.Props{"data-role": "last-updated"}, elem.Text(fmt.Sprintf("Last updated: %s", state.LastUpdated.Format("15:04:05")))),
				),
				elem.Div(attrs.Props{attrs.Class: "connection-status"},
					elem.Span(attrs.Props{"data-role": "connection-indicator", attrs.Class: "connection-indicator " + connectionIndicator}),
					elem.Span(attrs.Props{"data-role": "connection-text"}, elem.Text(connectionText)),
				),
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
		statusClass, cardChildren = ws.renderLightbulb(deviceID, info, state, cardChildren)
	case devices.DeviceTypeOutlet, devices.DeviceTypeSwitch:
		statusClass, cardChildren = ws.renderOutlet(deviceID, info, state, cardChildren)
	case devices.DeviceTypeFan:
		statusClass, cardChildren = ws.renderFan(deviceID, info, state, cardChildren)
	}

	return elem.Div(
		attrs.Props{
			attrs.ID:         "device-" + deviceID,
			attrs.Class:      "device " + statusClass,
			"data-device-id": deviceID,
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

	if info.Features.Temperature && state.Temperature != nil {
		items = append(items,
			elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
				elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Temperature:")),
				elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "temperature-value"},
					elem.Text(fmt.Sprintf("%.1f °C", *state.Temperature)),
				),
			),
		)
	}

	if info.Features.Humidity && state.Humidity != nil {
		items = append(items,
			elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
				elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Humidity:")),
				elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "humidity-value"},
					elem.Text(fmt.Sprintf("%.1f %%", *state.Humidity)),
				),
			),
		)
	}

	if info.Features.Battery && state.Battery != nil {
		items = append(items,
			elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
				elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Battery:")),
				elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "battery-value"},
					elem.Text(fmt.Sprintf("%d %%", *state.Battery)),
				),
			),
		)
	}

	if info.Features.Pressure && state.Pressure != nil {
		items = append(items,
			elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
				elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Pressure:")),
				elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "pressure-value"},
					elem.Text(fmt.Sprintf("%.1f hPa", *state.Pressure)),
				),
			),
		)
	}

	return elem.Div(attrs.Props{attrs.Class: "sensor-values"}, items...)
}

func (ws *WebServer) renderOccupancySensor(info devices.Device, state devices.State) elem.Node {
	var items []elem.Node

	occupancyText := "Unknown"
	if state.Occupancy != nil {
		if *state.Occupancy {
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

	if info.Features.Battery && state.Battery != nil {
		items = append(items,
			elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
				elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Battery:")),
				elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "battery-value"},
					elem.Text(fmt.Sprintf("%d %%", *state.Battery)),
				),
			),
		)
	}

	if info.Features.Illuminance && state.Illuminance != nil {
		items = append(items,
			elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
				elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Illuminance:")),
				elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "illuminance-value"},
					elem.Text(fmt.Sprintf("%d lux", *state.Illuminance)),
				),
			),
		)
	}

	return elem.Div(attrs.Props{attrs.Class: "sensor-values"}, items...)
}

func (ws *WebServer) renderContactSensor(info devices.Device, state devices.State) elem.Node {
	var items []elem.Node

	contactText := "Unknown"
	if state.Contact != nil {
		if *state.Contact {
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

	if info.Features.Battery && state.Battery != nil {
		items = append(items,
			elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
				elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Battery:")),
				elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "battery-value"},
					elem.Text(fmt.Sprintf("%d %%", *state.Battery)),
				),
			),
		)
	}

	return elem.Div(attrs.Props{attrs.Class: "sensor-values"}, items...)
}

func (ws *WebServer) renderLeakSensor(info devices.Device, state devices.State) elem.Node {
	var items []elem.Node

	leakText := "Unknown"
	if state.WaterLeak != nil {
		if *state.WaterLeak {
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

	if info.Features.Battery && state.Battery != nil {
		items = append(items,
			elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
				elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Battery:")),
				elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "battery-value"},
					elem.Text(fmt.Sprintf("%d %%", *state.Battery)),
				),
			),
		)
	}

	return elem.Div(attrs.Props{attrs.Class: "sensor-values"}, items...)
}

func (ws *WebServer) renderSmokeSensor(info devices.Device, state devices.State) elem.Node {
	var items []elem.Node

	smokeText := "Unknown"
	if state.Smoke != nil {
		if *state.Smoke {
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

	if info.Features.Battery && state.Battery != nil {
		items = append(items,
			elem.Div(attrs.Props{attrs.Class: "sensor-value-item"},
				elem.Span(attrs.Props{attrs.Class: "sensor-label"}, elem.Text("Battery:")),
				elem.Span(attrs.Props{attrs.Class: "sensor-value", "data-role": "battery-value"},
					elem.Text(fmt.Sprintf("%d %%", *state.Battery)),
				),
			),
		)
	}

	return elem.Div(attrs.Props{attrs.Class: "sensor-values"}, items...)
}

func (ws *WebServer) renderFan(deviceID string, info devices.Device, state devices.State, cardChildren []elem.Node) (string, []elem.Node) {
	statusClass := "off"
	statusText := "OFF"
	buttonClass := "on"
	buttonText := "Turn On"
	buttonAction := "on"

	if state.On != nil && *state.On {
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
				elem.Div(attrs.Props{"data-role": "last-updated"}, elem.Text(fmt.Sprintf("Last updated: %s", state.LastUpdated.Format("15:04:05")))),
			),
			ws.renderConnectionStatus(state),
		),
	)

	// Add fan controls if speed feature is enabled
	if info.Features.Speed && state.FanSpeed != nil {
		cardChildren = append(cardChildren,
			elem.Div(attrs.Props{attrs.Class: "light-controls"},
				elem.Div(attrs.Props{attrs.Class: "light-control-item"},
					elem.Span(attrs.Props{attrs.Class: "light-control-label"}, elem.Text("Speed:")),
					elem.Span(attrs.Props{attrs.Class: "light-control-value", "data-role": "fan-speed-value"},
						elem.Text(fmt.Sprintf("%d%%", *state.FanSpeed)),
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

func (ws *WebServer) renderLightbulb(deviceID string, info devices.Device, state devices.State, cardChildren []elem.Node) (string, []elem.Node) {
	statusClass := "off"
	statusText := "OFF"
	buttonClass := "on"
	buttonText := "Turn On"
	buttonAction := "on"

	if state.On != nil && *state.On {
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
				elem.Div(attrs.Props{"data-role": "last-updated"}, elem.Text(fmt.Sprintf("Last updated: %s", state.LastUpdated.Format("15:04:05")))),
			),
			ws.renderConnectionStatus(state),
		),
	)

	// Add light controls if applicable
	var lightItems []elem.Node

	if info.Features.Brightness && state.Brightness != nil {
		brightnessHAP := devices.Z2MBrightnessToHAP(*state.Brightness)
		lightItems = append(lightItems,
			elem.Div(attrs.Props{attrs.Class: "light-control-item brightness-slider-container"},
				elem.Span(attrs.Props{attrs.Class: "light-control-label"}, elem.Text("Brightness:")),
				elem.Span(attrs.Props{attrs.Class: "light-control-value", "data-role": "brightness-value"},
					elem.Text(fmt.Sprintf("%d%%", brightnessHAP)),
				),
				elem.Input(attrs.Props{
					attrs.Type:  "range",
					attrs.Class: "brightness-slider",
					attrs.Min:   "0",
					attrs.Max:   "100",
					attrs.Value: fmt.Sprintf("%d", brightnessHAP),
					attrs.Name:  "brightness",
					"data-device-id":   deviceID,
					"data-role":        "brightness-slider",
					"hx-post":          "/brightness/" + deviceID,
					"hx-trigger":       "change",
					"hx-target":        "#device-" + deviceID,
					"hx-swap":          "outerHTML",
					"hx-include":       "this",
				}),
			),
		)
	}

	if info.Features.Color && state.Hue != nil {
		lightItems = append(lightItems,
			elem.Div(attrs.Props{attrs.Class: "light-control-item"},
				elem.Span(attrs.Props{attrs.Class: "light-control-label"}, elem.Text("Hue:")),
				elem.Span(attrs.Props{attrs.Class: "light-control-value", "data-role": "hue-value"},
					elem.Text(fmt.Sprintf("%.0f°", *state.Hue)),
				),
			),
		)
	}

	if info.Features.Color && state.Saturation != nil {
		lightItems = append(lightItems,
			elem.Div(attrs.Props{attrs.Class: "light-control-item"},
				elem.Span(attrs.Props{attrs.Class: "light-control-label"}, elem.Text("Saturation:")),
				elem.Span(attrs.Props{attrs.Class: "light-control-value", "data-role": "saturation-value"},
					elem.Text(fmt.Sprintf("%.0f%%", *state.Saturation)),
				),
			),
		)
	}

	if info.Features.ColorTemperature && state.ColorTemp != nil {
		lightItems = append(lightItems,
			elem.Div(attrs.Props{attrs.Class: "light-control-item"},
				elem.Span(attrs.Props{attrs.Class: "light-control-label"}, elem.Text("Color Temp:")),
				elem.Span(attrs.Props{attrs.Class: "light-control-value", "data-role": "color-temp-value"},
					elem.Text(fmt.Sprintf("%d mireds", *state.ColorTemp)),
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

func (ws *WebServer) renderOutlet(deviceID string, info devices.Device, state devices.State, cardChildren []elem.Node) (string, []elem.Node) {
	statusClass := "off"
	statusText := "OFF"
	buttonClass := "on"
	buttonText := "Turn On"
	buttonAction := "on"

	if state.On != nil && *state.On {
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
				elem.Div(attrs.Props{"data-role": "last-updated"}, elem.Text(fmt.Sprintf("Last updated: %s", state.LastUpdated.Format("15:04:05")))),
			),
			ws.renderConnectionStatus(state),
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

func (ws *WebServer) renderConnectionStatus(state devices.State) elem.Node {
	var connectionIndicator, connectionText string
	if state.LastSeen.IsZero() {
		connectionIndicator = "disconnected"
		connectionText = "Never seen"
	} else {
		timeSinceSeen := time.Since(state.LastSeen)
		if timeSinceSeen < 30*time.Second {
			connectionIndicator = "connected"
			connectionText = fmt.Sprintf("Last seen: %s ago", timeSinceSeen.Round(time.Second))
		} else if timeSinceSeen < 60*time.Second {
			connectionIndicator = "stale"
			connectionText = fmt.Sprintf("Last seen: %s ago", timeSinceSeen.Round(time.Second))
		} else {
			connectionIndicator = "disconnected"
			connectionText = fmt.Sprintf("Last seen: %s ago", timeSinceSeen.Round(time.Second))
		}
	}

	return elem.Div(attrs.Props{attrs.Class: "connection-status"},
		elem.Span(attrs.Props{"data-role": "connection-indicator", attrs.Class: "connection-indicator " + connectionIndicator}),
		elem.Span(attrs.Props{"data-role": "connection-text"}, elem.Text(connectionText)),
	)
}

// HandleIndex renders the main dashboard
func (ws *WebServer) HandleIndex(w http.ResponseWriter, r *http.Request) {
	var deviceElements []elem.Node

	snapshot := ws.deviceProvider.Snapshot()
	var deviceIDs []string
	for id := range snapshot {
		deviceIDs = append(deviceIDs, id)
	}
	sort.Strings(deviceIDs)

	for _, id := range deviceIDs {
		item := snapshot[id]
		if item.Device.Web != nil && !*item.Device.Web {
			continue
		}
		deviceElements = append(deviceElements, ws.renderDeviceCard(id, item.Device, item.State))
	}

	var eventElements []elem.Node
	for i := len(ws.eventLog) - 1; i >= 0 && i >= len(ws.eventLog)-20; i-- {
		eventElements = append(eventElements, elem.Div(attrs.Props{attrs.Class: "event"}, elem.Text(ws.eventLog[i])))
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
		elem.P(attrs.Props{}, elem.Text(fmt.Sprintf("Managing %d devices", len(snapshot)))),
		homekitSection,
		elem.Div(attrs.Props{attrs.Class: "devices-grid"}, deviceElements...),
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

	device, state, exists := ws.deviceProvider.Device(deviceID)
	if !exists {
		http.Error(w, "Device not found", http.StatusNotFound)
		return
	}

	if device.Web != nil && !*device.Web {
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
		if updatedDevice, updatedState, ok := ws.deviceProvider.Device(deviceID); ok {
			device = updatedDevice
			state = updatedState
		}

		w.Header().Set("Content-Type", "text/html")
		if _, err := fmt.Fprint(w, ws.renderDeviceCard(deviceID, device, state).Render()); err != nil {
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

	device, state, exists := ws.deviceProvider.Device(deviceID)
	if !exists {
		http.Error(w, "Device not found", http.StatusNotFound)
		return
	}

	if device.Web != nil && !*device.Web {
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
	if brightness < 0 {
		brightness = 0
	}
	if brightness > 100 {
		brightness = 100
	}

	if err := ws.controller.SetBrightness(r.Context(), deviceID, brightness); err != nil {
		ws.logger.Error("Failed to set brightness", "device_id", deviceID, "error", err)
		http.Error(w, "Failed to set brightness", http.StatusInternalServerError)
		return
	}

	ws.LogEvent(fmt.Sprintf("Web UI: Brightness %s -> %d%%", deviceID, brightness))

	if r.Header.Get("HX-Request") == "true" {
		if updatedDevice, updatedState, ok := ws.deviceProvider.Device(deviceID); ok {
			device = updatedDevice
			state = updatedState
		}

		w.Header().Set("Content-Type", "text/html")
		if _, err := fmt.Fprint(w, ws.renderDeviceCard(deviceID, device, state).Render()); err != nil {
			ws.logger.Error("Failed to write response", slog.Any("error", err))
		}
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// HandleEventBusDebug renders a simple diagnostic view of the current state map.
func (ws *WebServer) HandleEventBusDebug(w http.ResponseWriter, r *http.Request) {
	snapshot := ws.snapshotState()

	ws.sseClientsMu.RLock()
	clientCount := len(ws.sseClients)
	ws.sseClientsMu.RUnlock()

	rows := []elem.Node{
		elem.Tr(attrs.Props{},
			elem.Th(attrs.Props{}, elem.Text("Device ID")),
			elem.Th(attrs.Props{}, elem.Text("Name")),
			elem.Th(attrs.Props{}, elem.Text("On")),
			elem.Th(attrs.Props{}, elem.Text("Last Updated")),
			elem.Th(attrs.Props{}, elem.Text("Last Seen")),
			elem.Th(attrs.Props{}, elem.Text("Connection")),
		),
	}

	for _, evt := range snapshot {
		onText := "n/a"
		if evt.On != nil {
			onText = fmt.Sprintf("%t", *evt.On)
		}
		rows = append(rows,
			elem.Tr(attrs.Props{},
				elem.Td(attrs.Props{}, elem.Text(evt.DeviceID)),
				elem.Td(attrs.Props{}, elem.Text(evt.Name)),
				elem.Td(attrs.Props{}, elem.Text(onText)),
				elem.Td(attrs.Props{}, elem.Text(evt.LastUpdated.Format(time.RFC3339))),
				elem.Td(attrs.Props{}, elem.Text(evt.LastSeen.Format(time.RFC3339))),
				elem.Td(attrs.Props{}, elem.Text(evt.ConnectionNote)),
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
		elem.P(attrs.Props{}, elem.Text(fmt.Sprintf("Connected SSE clients: %d", clientCount))),
		elem.Table(attrs.Props{"border": "1", "cellpadding": "4", "cellspacing": "0"}, rows...),
		elem.H2(attrs.Props{}, elem.Text("Component Status")),
		elem.Table(attrs.Props{"border": "1", "cellpadding": "4", "cellspacing": "0"}, statusRows...),
	)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := fmt.Fprint(w, ws.renderPage("EventBus Debug", content)); err != nil {
		ws.logger.Error("Failed to write eventbus debug response", slog.Any("error", err))
	}
}

// HandleSSE streams JSON state updates to clients.
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

	clientChan := make(chan events.StateUpdateEvent, 10)

	ws.sseClientsMu.Lock()
	ws.sseClients[clientChan] = struct{}{}
	ws.sseClientsMu.Unlock()

	defer func() {
		ws.sseClientsMu.Lock()
		delete(ws.sseClients, clientChan)
		ws.sseClientsMu.Unlock()
		close(clientChan)
	}()

	for _, evt := range ws.snapshotState() {
		select {
		case clientChan <- evt:
		default:
		}
	}

	for {
		select {
		case evt := <-clientChan:
			payload, err := json.Marshal(evt)
			if err != nil {
				ws.logger.Error("Failed to marshal SSE payload", slog.Any("error", err))
				continue
			}

			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()

		case <-r.Context().Done():
			return
		case <-ws.ctx.Done():
			return
		}
	}
}

// HandleHealth exposes a JSON health summary.
func (ws *WebServer) HandleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	snapshot := ws.deviceProvider.Snapshot()

	ws.sseClientsMu.RLock()
	sseClients := len(ws.sseClients)
	ws.sseClientsMu.RUnlock()

	resp := struct {
		Status     string    `json:"status"`
		Devices    int       `json:"devices"`
		SSEClients int       `json:"sse_clients"`
		Timestamp  time.Time `json:"timestamp"`
	}{
		Status:     "ok",
		Devices:    len(snapshot),
		SSEClients: sseClients,
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
