package z2mhomekit

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	homekitqr "github.com/kradalby/homekit-qr"
	"github.com/kradalby/kra/web"
	appconfig "github.com/kradalby/z2m-homekit/config"
	"github.com/kradalby/z2m-homekit/devices"
	"github.com/kradalby/z2m-homekit/events"
	"github.com/kradalby/z2m-homekit/logging"
	"github.com/kradalby/z2m-homekit/metrics"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"

	"github.com/brutella/hap"
	"tailscale.com/util/eventbus"
)

var version = "dev"

// getLocalIP returns the local IP address to use for MQTT broker configuration
func getLocalIP() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String(), nil
			}
		}
	}

	return "", fmt.Errorf("no local IP address found")
}

// Main is the entry point used by cmd/z2m-homekit.
func Main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	cfg, err := appconfig.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	logger, err := logging.New(cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to configure logger: %v\n", err)
		os.Exit(1)
	}
	slog.SetDefault(logger)

	slog.Info("Starting z2m-homekit Bridge",
		"version", version,
		"log_level", cfg.LogLevel,
		"log_format", cfg.LogFormat,
	)

	slog.Info("Configuration loaded",
		"hap_addr", cfg.HAPAddrPort().String(),
		"web_addr", cfg.WebAddrPort().String(),
		"mqtt_addr", cfg.MQTTAddrPort().String(),
		"devices_config", cfg.DevicesConfigPath,
	)

	deviceCfg, err := devices.LoadConfig(cfg.DevicesConfigPath)
	if err != nil {
		slog.Error("Failed to load devices configuration", "error", err)
		os.Exit(1)
	}

	slog.Info("Loaded devices", "count", len(deviceCfg.Devices))
	for _, device := range deviceCfg.Devices {
		slog.Info("Device configured",
			"id", device.ID,
			"name", device.Name,
			"type", device.Type,
			"topic", device.Topic,
		)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	eventBus, err := events.New(logger)
	if err != nil {
		slog.Error("Failed to initialize eventbus", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := eventBus.Close(); err != nil {
			slog.Warn("Error closing eventbus", "error", err)
		}
	}()

	// Initialize metrics collector
	metricsCollector, err := metrics.NewCollector(ctx, logger, eventBus, nil)
	if err != nil {
		slog.Error("Failed to initialize metrics collector", "error", err)
		os.Exit(1)
	}
	defer metricsCollector.Close()

	commands := make(chan devices.CommandEvent, 10)

	localIP, err := getLocalIP()
	if err != nil {
		slog.Warn("Failed to get local IP, using localhost", "error", err)
		localIP = "localhost"
	}
	slog.Info("Local IP address", "ip", localIP)

	// Create MQTT server
	mqttServer := mqtt.New(&mqtt.Options{
		InlineClient: true,
	})

	if err := mqttServer.AddHook(new(auth.AllowHook), nil); err != nil {
		slog.Error("Failed to add MQTT auth hook", "error", err)
		os.Exit(1)
	}

	// Create device manager
	deviceManager, err := devices.NewManager(deviceCfg.Devices, commands, eventBus, mqttServer, logger)
	if err != nil {
		slog.Error("Failed to initialize device manager", "error", err)
		os.Exit(1)
	}

	// Add MQTT hook for message processing
	mqttClient, err := eventBus.Client(events.ClientMQTT)
	if err != nil {
		slog.Error("Failed to get MQTT client", "error", err)
		os.Exit(1)
	}
	mqttHook := &MQTTHook{
		statePublisher: eventbus.Publish[devices.StateChangedEvent](mqttClient),
		deviceManager:  deviceManager,
		logger:         logger,
	}
	if err := mqttServer.AddHook(mqttHook, nil); err != nil {
		slog.Error("Failed to add MQTT message hook", "error", err)
		os.Exit(1)
	}

	tcp := listeners.NewTCP(listeners.Config{
		ID:      "tcp",
		Address: cfg.MQTTAddrPort().String(),
	})
	if err := mqttServer.AddListener(tcp); err != nil {
		slog.Error("Failed to add MQTT listener", "error", err)
		os.Exit(1)
	}

	mqttComponent := string(events.ClientMQTT)
	eventBus.PublishConnectionStatus(mqttClient, events.ConnectionStatusEvent{
		Timestamp: time.Now(),
		Component: mqttComponent,
		Status:    events.ConnectionStatusConnecting,
	})

	go func() {
		slog.Info("Starting MQTT broker", "addr", cfg.MQTTAddrPort().String())
		eventBus.PublishConnectionStatus(mqttClient, events.ConnectionStatusEvent{
			Timestamp: time.Now(),
			Component: mqttComponent,
			Status:    events.ConnectionStatusConnected,
		})
		if err := mqttServer.Serve(); err != nil {
			eventBus.PublishConnectionStatus(mqttClient, events.ConnectionStatusEvent{
				Timestamp: time.Now(),
				Component: mqttComponent,
				Status:    events.ConnectionStatusFailed,
				Error:     err.Error(),
			})
			slog.Error("MQTT server error", "error", err)
			return
		}
		eventBus.PublishConnectionStatus(mqttClient, events.ConnectionStatusEvent{
			Timestamp: time.Now(),
			Component: mqttComponent,
			Status:    events.ConnectionStatusDisconnected,
		})
	}()

	slog.Info("MQTT broker started", "addr", cfg.MQTTAddrPort().String())

	go deviceManager.ProcessCommands(ctx)
	go deviceManager.ProcessStateEvents(ctx)

	// Create HAP manager
	hapManager := NewHAPManager(deviceCfg.Devices, cfg.BridgeName, commands, deviceManager, eventBus, logger)
	hapManager.Start(ctx)
	defer hapManager.Close()

	accessories := hapManager.GetAccessories()
	if len(accessories) == 0 {
		slog.Error("No accessories to serve")
		os.Exit(1)
	}

	fsStore := hap.NewFsStore(cfg.HAPStoragePath)
	hapServer, err := hap.NewServer(
		fsStore,
		accessories[0],
		accessories[1:]...,
	)
	if err != nil {
		slog.Error("Failed to create HAP server", "error", err)
		os.Exit(1)
	}

	hapServer.Pin = cfg.HAPPin
	hapServer.Addr = cfg.HAPAddrPort().String()

	hapManager.SetServer(hapServer)
	hapManager.SetStore(fsStore)

	hapStatusClient, err := eventBus.Client(events.ClientHAP)
	if err != nil {
		slog.Error("Failed to get HAP client", "error", err)
		os.Exit(1)
	}
	hapComponent := string(events.ClientHAP)
	eventBus.PublishConnectionStatus(hapStatusClient, events.ConnectionStatusEvent{
		Timestamp: time.Now(),
		Component: hapComponent,
		Status:    events.ConnectionStatusConnecting,
	})

	go func() {
		slog.Info("Starting HomeKit server",
			"addr", cfg.HAPAddrPort().String(),
			"pin", cfg.HAPPin,
		)
		eventBus.PublishConnectionStatus(hapStatusClient, events.ConnectionStatusEvent{
			Timestamp: time.Now(),
			Component: hapComponent,
			Status:    events.ConnectionStatusConnected,
		})
		if err := hapServer.ListenAndServe(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				eventBus.PublishConnectionStatus(hapStatusClient, events.ConnectionStatusEvent{
					Timestamp: time.Now(),
					Component: hapComponent,
					Status:    events.ConnectionStatusDisconnected,
				})
			} else {
				eventBus.PublishConnectionStatus(hapStatusClient, events.ConnectionStatusEvent{
					Timestamp: time.Now(),
					Component: hapComponent,
					Status:    events.ConnectionStatusFailed,
					Error:     err.Error(),
				})
				slog.Error("HAP server error", "error", err)
			}
			return
		}
		eventBus.PublishConnectionStatus(hapStatusClient, events.ConnectionStatusEvent{
			Timestamp: time.Now(),
			Component: hapComponent,
			Status:    events.ConnectionStatusDisconnected,
		})
	}()

	fmt.Printf("HomeKit bridge ready - pair with PIN: %s\n\n", cfg.HAPPin)

	qrConfig := homekitqr.QRCodeConfig{
		SetupURIConfig: homekitqr.SetupURIConfig{
			PairingCode: cfg.HAPPin,
			SetupID:     "Z2MH",
			Category:    homekitqr.CategoryBridge,
		},
	}

	qr, err := homekitqr.GenerateQRTerminal(qrConfig)
	if err != nil {
		slog.Warn("Failed to generate QR code", "error", err)
	} else {
		fmt.Println(qr)
	}

	fmt.Println("========================================")
	slog.Info("Scan QR code or enter PIN manually in Home app", "pin", cfg.HAPPin)

	qrCode := ""
	if qr != "" {
		qrCode = qr
	}

	kraOpts := []web.Option{
		web.WithStdLogger(log.New(os.Stdout, "kraweb: ", log.LstdFlags)),
		web.WithLogger(logger),
		web.WithTailscaleStateDir(cfg.TailscaleStateDir),
	}

	enableTailscale := cfg.TailscaleAuthKey != ""
	kraConfig := web.ServerConfig{
		Hostname:        cfg.TailscaleHostname,
		LocalAddr:       cfg.WebAddrPort().String(),
		AuthKey:         cfg.TailscaleAuthKey,
		EnableTailscale: enableTailscale,
	}

	kraWeb, err := web.NewServer(kraConfig, kraOpts...)
	if err != nil {
		slog.Error("Failed to configure web server", "error", err)
		os.Exit(1)
	}

	webServer := NewWebServer(logger, deviceManager, deviceManager, eventBus, kraWeb, cfg.HAPPin, qrCode, hapManager)
	webServer.LogEvent("Server starting...")
	webServer.Start(ctx)
	defer webServer.Close()

	kraWeb.Handle("/", http.HandlerFunc(webServer.HandleIndex))
	kraWeb.Handle("/toggle/", http.HandlerFunc(webServer.HandleToggle))
	kraWeb.Handle("/brightness/", http.HandlerFunc(webServer.HandleBrightness))
	kraWeb.Handle("/events", http.HandlerFunc(webServer.HandleSSE))
	kraWeb.Handle("/health", http.HandlerFunc(webServer.HandleHealth))
	kraWeb.Handle("/qrcode", http.HandlerFunc(webServer.HandleQRCode))
	kraWeb.Handle("/debug/eventbus", http.HandlerFunc(webServer.HandleEventBusDebug))
	kraWeb.Handle("/metrics", promhttp.Handler())

	// Setup debug handlers
	SetupDebugHandlers(kraWeb, hapManager)

	webURL := fmt.Sprintf("http://%s", cfg.WebAddrPort().String())
	if enableTailscale {
		webURL = fmt.Sprintf("https://%s (and http://%s)", cfg.TailscaleHostname, cfg.WebAddrPort().String())
	}
	slog.Info("Web UI available", "url", webURL)

	slog.Info("Server running, press Ctrl+C to stop")
	<-ctx.Done()
	slog.Info("Shutting down...")

	slog.Info("Stopping web server...")
	slog.Info("Stopping MQTT broker...")
	if err := mqttServer.Close(); err != nil {
		slog.Error("Error stopping MQTT broker", "error", err)
	}
	eventBus.PublishConnectionStatus(mqttClient, events.ConnectionStatusEvent{
		Timestamp: time.Now(),
		Component: mqttComponent,
		Status:    events.ConnectionStatusDisconnected,
	})
	slog.Info("Shutdown complete")
}
