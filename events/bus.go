package events

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"tailscale.com/util/eventbus"
)

// ClientName represents named clients used on the shared event bus.
type ClientName string

const (
	ClientDeviceManager ClientName = "devicemanager"
	ClientHAP           ClientName = "hap"
	ClientWeb           ClientName = "web"
	ClientMQTT          ClientName = "mqtt"
	ClientMetrics       ClientName = "metrics"
)

// clientPublishers holds the long-lived publishers for one client.
//
// eventbus.Publish panics when handed a closed client, so a publisher must not
// be created per event: shutdown closes the clients while HAP and web are still
// emitting their final connection-status events. Creating them once up front
// turns that race into a no-op -- publishing on a closed Publisher is defined
// to do nothing -- and keeps the hot state path off the bus's publisher set.
type clientPublishers struct {
	state  *eventbus.Publisher[StateUpdateEvent]
	cmd    *eventbus.Publisher[CommandEvent]
	status *eventbus.Publisher[ConnectionStatusEvent]
}

// Bus wraps tailscale's eventbus and provides helpers for publishing state updates.
type Bus struct {
	bus     *eventbus.Bus
	clients map[ClientName]*eventbus.Client
	pubs    map[*eventbus.Client]clientPublishers
	logger  *slog.Logger
	ctx     context.Context
	cancel  context.CancelFunc

	lastStates map[string]StateUpdateEvent
	stateMu    sync.Mutex
	mu         sync.RWMutex
}

// New constructs a new bus with the known clients registered.
func New(logger *slog.Logger) (*Bus, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}

	ctx, cancel := context.WithCancel(context.Background())

	b := &Bus{
		bus:        eventbus.New(),
		clients:    make(map[ClientName]*eventbus.Client),
		pubs:       make(map[*eventbus.Client]clientPublishers),
		logger:     logger,
		ctx:        ctx,
		cancel:     cancel,
		lastStates: make(map[string]StateUpdateEvent),
	}

	for _, name := range []ClientName{
		ClientDeviceManager,
		ClientHAP,
		ClientWeb,
		ClientMQTT,
		ClientMetrics,
	} {
		client := b.bus.Client(string(name))
		b.clients[name] = client
		b.pubs[client] = clientPublishers{
			state:  eventbus.Publish[StateUpdateEvent](client),
			cmd:    eventbus.Publish[CommandEvent](client),
			status: eventbus.Publish[ConnectionStatusEvent](client),
		}
	}

	logger.Info("eventbus initialized",
		slog.Int("client_count", len(b.clients)),
	)

	return b, nil
}

// Client returns the named eventbus client.
func (b *Bus) Client(name ClientName) (*eventbus.Client, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	client, ok := b.clients[name]
	if !ok {
		return nil, fmt.Errorf("client %q not found", name)
	}

	return client, nil
}

// publishers returns the long-lived publishers for client, or false once the
// bus has been shut down.
func (b *Bus) publishers(client *eventbus.Client) (clientPublishers, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	p, ok := b.pubs[client]

	return p, ok
}

// PublishStateUpdate emits a deduplicated state update event for SSE consumers.
func (b *Bus) PublishStateUpdate(client *eventbus.Client, event StateUpdateEvent) {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()

	last, ok := b.lastStates[event.DeviceID]
	if ok && event.Equals(last) {
		b.logger.Debug("skipping duplicate state update",
			slog.String("device_id", event.DeviceID),
			slog.String("source", event.Source),
		)
		return
	}

	b.logger.Debug("publishing state update",
		slog.String("device_id", event.DeviceID),
		slog.String("source", event.Source),
	)

	if p, ok := b.publishers(client); ok {
		p.state.Publish(event)
	}

	b.lastStates[event.DeviceID] = event
}

// PublishCommand emits a command event for metrics/debug consumers.
func (b *Bus) PublishCommand(client *eventbus.Client, event CommandEvent) {
	b.logger.Debug("publishing command event",
		slog.String("device_id", event.DeviceID),
		slog.String("source", event.Source),
		slog.String("command_type", string(event.CommandType)),
	)

	if p, ok := b.publishers(client); ok {
		p.cmd.Publish(event)
	}
}

// PublishConnectionStatus emits lifecycle updates for components (web, hap, mqtt, etc.).
func (b *Bus) PublishConnectionStatus(client *eventbus.Client, event ConnectionStatusEvent) {
	b.logger.Debug("publishing connection status",
		slog.String("component", event.Component),
		slog.String("status", string(event.Status)),
	)

	if p, ok := b.publishers(client); ok {
		p.status.Publish(event)
	}
}

// Close shuts down the event bus and releases clients.
func (b *Bus) Close() error {
	b.cancel()

	b.mu.Lock()
	defer b.mu.Unlock()

	// eventbus.Bus.Close stops the bus's router goroutine and closes every
	// client it handed out. Closing only the clients, as this used to, leaks
	// that goroutine for the lifetime of the process (and once per test).
	b.bus.Close()
	clear(b.clients)
	clear(b.pubs)

	b.logger.Info("eventbus shut down")
	return nil
}
