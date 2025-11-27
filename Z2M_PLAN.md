# Zigbee2MQTT HomeKit Bridge (z2m-homekit) - Implementation Plan

This document outlines the comprehensive implementation plan for a Golang service that bridges Zigbee2MQTT devices to Apple HomeKit, replacing the current homebridge/mqttthing setup.

---

## 1. Project Overview

### 1.1 Purpose

Replace the homebridge + mqttthing Node.js-based solution with a native Go application that:
- Hosts an embedded MQTT broker for zigbee2mqtt
- Exposes devices to Apple HomeKit
- Provides a web UI for monitoring and control
- Uses an event bus for internal communication
- Integrates with Tailscale for remote access

### 1.2 Reference Implementation

Based on `github.com/kradalby/tasmota-homekit` architecture and patterns from the DEV_GUIDE.md.

---

## 2. Data Flow Architecture

```
                                     z2m-homekit Service
┌─────────────────────────────────────────────────────────────────────────────────┐
│                                                                                 │
│  ┌──────────────────┐                                                           │
│  │  Configuration   │                                                           │
│  │  (devices.hujson)│                                                           │
│  └────────┬─────────┘                                                           │
│           │                                                                     │
│           ▼                                                                     │
│  ┌──────────────────┐       ┌──────────────────────────────────────────────┐   │
│  │  Device Manager  │◄─────►│            Event Bus (tailscale)              │   │
│  │  (devices/)      │       │                                              │   │
│  └────────┬─────────┘       │  StateUpdateEvent    CommandEvent            │   │
│           │                 │  ConnectionStatusEvent                       │   │
│           │                 └───────────┬─────────────┬───────────────────┘   │
│           │                             │             │                        │
│  ┌────────▼─────────┐       ┌───────────▼───┐ ┌──────▼───────┐                │
│  │   MQTT Broker    │       │  HAP Manager  │ │  Web Server  │                │
│  │   (mochi-mqtt)   │       │  (brutella)   │ │  (kra/web)   │                │
│  │                  │       │               │ │              │                │
│  │  - Listens 1883  │       │  - Listens    │ │  - HTTP UI   │                │
│  │  - Publishes/    │       │    51826      │ │  - SSE       │                │
│  │    Subscribes    │       │  - Accessories│ │  - Debug     │                │
│  └────────┬─────────┘       │  - Commands   │ │  - Metrics   │                │
│           │                 └───────┬───────┘ └──────┬───────┘                │
│           │                         │                │                        │
└───────────┼─────────────────────────┼────────────────┼────────────────────────┘
            │                         │                │
            ▼                         ▼                ▼
    ┌───────────────┐         ┌────────────┐   ┌─────────────┐
    │  zigbee2mqtt  │         │  Home App  │   │   Browser   │
    │  (external)   │         │  (Apple)   │   │   (Web UI)  │
    └───────┬───────┘         └────────────┘   └─────────────┘
            │
            ▼
    ┌───────────────┐
    │  Zigbee       │
    │  Coordinator  │
    │  (CC2531,etc) │
    └───────┬───────┘
            │
            ▼
    ┌───────────────┐
    │  Zigbee       │
    │  Devices      │
    │  (Aqara,      │
    │   IKEA,etc)   │
    └───────────────┘
```

### 2.1 Event Flow Detail

```
1. MQTT Message Received (zigbee2mqtt/<device>)
   │
   ├──► MQTTHook.OnPublish() parses JSON payload
   │    │
   │    └──► Publish StateChangedEvent to eventbus
   │
   ▼
2. Event Bus distributes to subscribers:
   │
   ├──► DeviceManager.ProcessStateEvents()
   │    - Updates internal state map
   │    - Publishes StateUpdateEvent for UI/HAP
   │
   ├──► HAPManager.ProcessStateChanges()
   │    - Updates HomeKit accessory values
   │    - Notifies Home.app of changes
   │
   └──► WebServer.processStateChanges()
        - Updates currentState map
        - Broadcasts via SSE to browsers

3. HomeKit Command (Home.app toggle)
   │
   ├──► HAPManager.OnValueRemoteUpdate()
   │    │
   │    └──► Send CommandEvent to devices channel
   │
   ▼
4. Command Processing:
   │
   ├──► DeviceManager.ProcessCommands()
   │    │
   │    └──► Publish to MQTT: zigbee2mqtt/<device>/set
   │         {"state": "ON"} or {"brightness": 100}
   │
   └──► zigbee2mqtt receives and controls device
```

---

## 3. Module Structure

```
z2m-homekit/
├── cmd/
│   └── z2m-homekit/
│       └── main.go              # Entry point
├── config/
│   ├── config.go                # Environment variable loading (go-env)
│   └── config_test.go
├── devices/
│   ├── types.go                 # Device types, config structures
│   ├── manager.go               # Device state management
│   ├── manager_test.go
│   └── accessories/             # HomeKit accessory implementations
│       ├── temperature.go       # TemperatureSensor
│       ├── humidity.go          # HumiditySensor
│       ├── occupancy.go         # OccupancySensor
│       ├── lightbulb.go         # Lightbulb (dimmable, color, CT)
│       ├── outlet.go            # Outlet/Switch
│       └── accessory_test.go
├── events/
│   ├── bus.go                   # Event bus wrapper
│   └── types.go                 # Event type definitions
├── logging/
│   └── logger.go                # slog configuration
├── metrics/
│   ├── collector.go             # Prometheus metrics
│   └── collector_test.go
├── nix/
│   ├── module.nix               # NixOS service module
│   └── test.nix                 # NixOS VM test
├── app.go                       # Main application orchestration
├── mqtt.go                      # MQTT broker + hook implementation
├── mqtt_test.go
├── hap.go                       # HomeKit manager
├── hap_test.go
├── web.go                       # Web UI server
├── web_test.go
├── debug.go                     # Debug handlers (tsweb)
├── flake.nix                    # Nix flake
├── go.mod
├── go.sum
├── DEV_GUIDE.md                 # Development standards
└── Z2M_PLAN.md                  # This file
```

---

## 4. Device Types & HomeKit Mapping

### 4.1 Supported Zigbee2MQTT Device Types

Based on current homebridge.nix configuration:

| Device Type | Zigbee2MQTT Topic Pattern | HomeKit Service | Characteristics |
|-------------|---------------------------|-----------------|-----------------|
| Aqara Temperature/Humidity | `zigbee2mqtt/<device>` | TemperatureSensor | CurrentTemperature |
| Aqara Temperature/Humidity | `zigbee2mqtt/<device>` | HumiditySensor | CurrentRelativeHumidity |
| Aqara Motion Sensor | `zigbee2mqtt/<device>` | OccupancySensor | OccupancyDetected, BatteryLevel |
| IKEA Tradfri Bulb | `zigbee2mqtt/<device>` | Lightbulb | On, Brightness |
| IKEA Tradfri Color Bulb | `zigbee2mqtt/<device>` | Lightbulb | On, Brightness, Hue, Saturation |
| IKEA Tradfri CT Bulb | `zigbee2mqtt/<device>` | Lightbulb | On, Brightness, ColorTemperature |

### 4.2 Zigbee2MQTT Message Formats

#### State Messages (zigbee2mqtt/<device>)

```json
// Temperature/Humidity Sensor (Aqara WSDCGQ11LM)
{
  "battery": 97,
  "humidity": 58.42,
  "linkquality": 142,
  "pressure": 1013.2,
  "temperature": 22.5,
  "voltage": 3015
}

// Motion/Occupancy Sensor (Aqara RTCGQ11LM)
{
  "battery": 100,
  "illuminance": 132,
  "illuminance_lux": 132,
  "linkquality": 152,
  "occupancy": true,
  "voltage": 3055
}

// Lightbulb (IKEA Tradfri LED1545G12)
{
  "brightness": 254,
  "color_mode": "color_temp",
  "color_temp": 370,
  "linkquality": 92,
  "state": "ON"
}

// Color Lightbulb (IKEA Tradfri LED1624G9)
{
  "brightness": 254,
  "color": {
    "hue": 240,
    "saturation": 100,
    "x": 0.1567,
    "y": 0.1174
  },
  "color_mode": "xy",
  "linkquality": 84,
  "state": "ON"
}
```

#### Command Messages (zigbee2mqtt/<device>/set)

```json
// Turn on/off
{"state": "ON"}
{"state": "OFF"}

// Set brightness (0-254)
{"brightness": 200}
{"state": "ON", "brightness": 200}

// Set color temperature (153-500 mireds)
{"color_temp": 370}

// Set color (HSV or XY)
{"color": {"hue": 120, "saturation": 100}}
{"color": {"x": 0.4, "y": 0.3}}
```

### 4.3 HomeKit Characteristic Mappings

| Z2M Property | HomeKit Characteristic | Conversion |
|--------------|------------------------|------------|
| `temperature` | CurrentTemperature | Direct (Celsius) |
| `humidity` | CurrentRelativeHumidity | Direct (%) |
| `battery` | StatusLowBattery | < 20 = LOW_BATTERY |
| `battery` | BatteryLevel | Direct (%) |
| `occupancy` | OccupancyDetected | true = DETECTED |
| `state` | On | "ON" = true |
| `brightness` | Brightness | z2m(0-254) → HAP(0-100) |
| `color_temp` | ColorTemperature | Direct (mireds) |
| `color.hue` | Hue | Direct (0-360) |
| `color.saturation` | Saturation | Direct (0-100) |

---

## 5. Configuration

### 5.1 Environment Variables (config/)

| Variable | Default | Description |
|----------|---------|-------------|
| `Z2M_HOMEKIT_HAP_PIN` | `00102003` | HomeKit pairing PIN (8 digits) |
| `Z2M_HOMEKIT_HAP_STORAGE_PATH` | `./data/hap` | HAP persistent storage |
| `Z2M_HOMEKIT_HAP_ADDR` | - | Override HAP address (e.g., "0.0.0.0:51826") |
| `Z2M_HOMEKIT_HAP_BIND_ADDRESS` | `0.0.0.0` | HAP bind address |
| `Z2M_HOMEKIT_HAP_PORT` | `51826` | HAP port |
| `Z2M_HOMEKIT_WEB_ADDR` | - | Override web address |
| `Z2M_HOMEKIT_WEB_BIND_ADDRESS` | `0.0.0.0` | Web bind address |
| `Z2M_HOMEKIT_WEB_PORT` | `8081` | Web UI port |
| `Z2M_HOMEKIT_MQTT_ADDR` | - | Override MQTT address |
| `Z2M_HOMEKIT_MQTT_BIND_ADDRESS` | `0.0.0.0` | MQTT bind address |
| `Z2M_HOMEKIT_MQTT_PORT` | `1883` | MQTT broker port |
| `Z2M_HOMEKIT_BRIDGE_NAME` | `z2m-homekit` | HomeKit bridge name |
| `Z2M_HOMEKIT_TS_HOSTNAME` | `z2m-homekit` | Tailscale hostname |
| `Z2M_HOMEKIT_TS_AUTHKEY` | - | Tailscale auth key (enables tsnet) |
| `Z2M_HOMEKIT_TS_STATE_DIR` | `./data/tailscale` | Tailscale state directory |
| `Z2M_HOMEKIT_LOG_LEVEL` | `info` | Log level (debug, info, warn, error) |
| `Z2M_HOMEKIT_LOG_FORMAT` | `json` | Log format (json, console) |
| `Z2M_HOMEKIT_DEVICES_CONFIG` | `./devices.hujson` | Device configuration file |

### 5.2 Device Configuration File (devices.hujson)

HuJSON format for device definitions:

```hujson
{
  // z2m-homekit device configuration
  // Based on zigbee2mqtt network

  "devices": [
    // ==============================
    // Climate Sensors (Aqara)
    // ==============================
    {
      "id": "kitchen-aqara",
      "name": "Kitchen",
      "topic": "kitchen-aqara",
      "type": "climate_sensor",
      // Creates both temperature and humidity sensors
      "features": {
        "temperature": true,
        "humidity": true,
        "battery": true,
      },
    },
    {
      "id": "bathroom-aqara",
      "name": "Bathroom",
      "topic": "bathroom-aqara",
      "type": "climate_sensor",
      "features": {
        "temperature": true,
        "humidity": true,
        "battery": true,
      },
    },
    {
      "id": "office-aqara",
      "name": "Office",
      "topic": "office-aqara",
      "type": "climate_sensor",
      "features": {
        "temperature": true,
        "humidity": true,
        "battery": true,
      },
    },
    {
      "id": "living-room-aqara",
      "name": "Living Room",
      "topic": "living-room-aqara",
      "type": "climate_sensor",
      "features": {
        "temperature": true,
        "humidity": true,
        "battery": true,
      },
    },

    // ==============================
    // Motion Sensors
    // ==============================
    {
      "id": "office-motion",
      "name": "Office Motion",
      "topic": "office-motion",
      "type": "occupancy_sensor",
      "features": {
        "battery": true,
      },
    },

    // ==============================
    // Lightbulbs (IKEA Tradfri)
    // ==============================
    {
      "id": "living-room-speaker-light",
      "name": "Living Room Speaker",
      "topic": "living-room-speaker-light",
      "type": "lightbulb",
      "features": {
        "brightness": true,
        "color": true,  // RGB color support
      },
    },
    {
      "id": "living-inner-light",
      "name": "Living Room Ceiling Inner",
      "topic": "living-inner-light",
      "type": "lightbulb",
      "features": {
        "brightness": true,
      },
    },
    {
      "id": "living-window-light",
      "name": "Living Room Ceiling Window",
      "topic": "living-window-light",
      "type": "lightbulb",
      "features": {
        "brightness": true,
      },
    },
  ],
}
```

### 5.3 Device Type Definitions

```go
// devices/types.go

type DeviceType string

const (
    DeviceTypeClimateSensor   DeviceType = "climate_sensor"
    DeviceTypeOccupancySensor DeviceType = "occupancy_sensor"
    DeviceTypeLightbulb       DeviceType = "lightbulb"
    DeviceTypeOutlet          DeviceType = "outlet"
    DeviceTypeSwitch          DeviceType = "switch"
)

type DeviceFeatures struct {
    // Sensors
    Temperature bool `json:"temperature,omitempty"`
    Humidity    bool `json:"humidity,omitempty"`
    Battery     bool `json:"battery,omitempty"`
    Occupancy   bool `json:"occupancy,omitempty"`
    Illuminance bool `json:"illuminance,omitempty"`

    // Lights
    Brightness       bool `json:"brightness,omitempty"`
    Color            bool `json:"color,omitempty"`           // HSV color
    ColorTemperature bool `json:"color_temperature,omitempty"` // CT in mireds
}

type Device struct {
    ID       string         `json:"id"`
    Name     string         `json:"name"`
    Topic    string         `json:"topic"`        // zigbee2mqtt topic suffix
    Type     DeviceType     `json:"type"`
    Features DeviceFeatures `json:"features,omitempty"`
    HomeKit  *bool          `json:"homekit,omitempty"`  // default true
    Web      *bool          `json:"web,omitempty"`      // default true
}

type Config struct {
    Devices []Device `json:"devices"`
}
```

---

## 6. NixOS Module Options

### 6.1 Module Options (nix/module.nix)

```nix
{
  options.services.z2m-homekit = {
    enable = mkEnableOption "Zigbee2MQTT HomeKit bridge service";

    package = mkOption {
      type = types.package;
      description = "The z2m-homekit package to run.";
    };

    environmentFile = mkOption {
      type = types.nullOr types.path;
      default = null;
      description = "Optional environment file that provides Z2M_HOMEKIT_* variables.";
      example = "/run/secrets/z2m-homekit.env";
    };

    environment = mkOption {
      type = types.attrsOf types.str;
      default = { };
      description = "Additional environment variables to pass to the service.";
    };

    bridgeName = mkOption {
      type = types.nullOr types.str;
      default = null;
      description = ''
        Override the HomeKit bridge name. Defaults to the Tailscale hostname
        (or "z2m-homekit") when unset.
      '';
      example = "z2m-homekit-dev";
    };

    user = mkOption {
      type = types.str;
      default = "z2m-homekit";
      description = "User account under which the service runs.";
    };

    group = mkOption {
      type = types.str;
      default = "z2m-homekit";
      description = "Group under which the service runs.";
    };

    ports = {
      hap = mkOption {
        type = types.port;
        default = 51826;
        description = "Port for the HomeKit Accessory Protocol (HAP) server.";
      };

      web = mkOption {
        type = types.port;
        default = 8081;
        description = "Port for the web interface.";
      };

      mqtt = mkOption {
        type = types.port;
        default = 1883;
        description = "Port for the embedded MQTT broker.";
      };
    };

    bindAddresses = {
      hap = mkOption {
        type = types.str;
        default = "0.0.0.0";
        description = "Address to bind the HAP listener to.";
      };

      web = mkOption {
        type = types.str;
        default = "0.0.0.0";
        description = "Address to bind the web interface to.";
      };

      mqtt = mkOption {
        type = types.str;
        default = "0.0.0.0";
        description = "Address to bind the embedded MQTT broker to.";
      };
    };

    dataDir = mkOption {
      type = types.path;
      default = "/var/lib/z2m-homekit";
      description = "Base directory for persistent data (contains HAP + Tailscale state).";
      example = "/var/lib/z2m-homekit";
    };

    hap = {
      pin = mkOption {
        type = types.str;
        default = "00102003";
        description = "HomeKit pairing PIN (8 digits).";
        example = "12345678";
      };
    };

    devicesConfig = mkOption {
      type = types.path;
      description = "HuJSON configuration describing the managed devices.";
      example = "/etc/z2m-homekit/devices.hujson";
    };

    log = {
      level = mkOption {
        type = types.enum [ "debug" "info" "warn" "error" ];
        default = "info";
        description = "Logging level for the service.";
      };

      format = mkOption {
        type = types.enum [ "json" "console" ];
        default = "json";
        description = "Logging format.";
      };
    };

    tailscale = {
      hostname = mkOption {
        type = types.str;
        default = "z2m-homekit";
        description = "Hostname to advertise on Tailscale when enabled.";
      };

      authKeyFile = mkOption {
        type = types.nullOr types.path;
        default = null;
        description = ''
          Path to a file containing the Tailscale auth key. When set, the service
          exports Z2M_HOMEKIT_TS_AUTHKEY from the credential file.
        '';
        example = "/run/secrets/tailscale-authkey";
      };
    };

    openFirewall = mkOption {
      type = types.bool;
      default = false;
      description = "Open the service ports (HAP/Web/MQTT) and UDP 5353 for mDNS.";
    };

    zigbee2mqtt = {
      configureServer = mkOption {
        type = types.bool;
        default = true;
        description = ''
          Whether to automatically configure zigbee2mqtt to use this MQTT broker.
          When true, sets mqtt.server in zigbee2mqtt config.
        '';
      };

      serviceName = mkOption {
        type = types.str;
        default = "zigbee2mqtt";
        description = "Name of the zigbee2mqtt service (for integration).";
      };
    };
  };
}
```

### 6.2 Example NixOS Configuration

```nix
# machines/home.ldn/z2m-homekit.nix
{ config, pkgs, lib, ... }:

{
  services.z2m-homekit = {
    enable = true;
    package = pkgs.z2m-homekit;

    bridgeName = "Home Z2M Bridge";

    ports = {
      hap = 51826;
      web = 8081;
      mqtt = 1883;
    };

    hap.pin = "033-44-255";

    devicesConfig = ./devices.hujson;

    log.level = "info";

    tailscale = {
      hostname = "z2m-homekit-ldn";
      authKeyFile = config.age.secrets.tailscale-authkey.path;
    };

    openFirewall = true;
  };

  # Configure zigbee2mqtt to use embedded broker
  services.zigbee2mqtt.settings.mqtt = {
    server = "mqtt://127.0.0.1:1883";
  };

  # Tailscale service exposure
  services.tailscale.services."svc:z2m-homekit-ldn" = {
    endpoints = {
      "tcp:80" = "http://127.0.0.1:${toString config.services.z2m-homekit.ports.web}";
      "tcp:443" = "http://127.0.0.1:${toString config.services.z2m-homekit.ports.web}";
    };
  };
}
```

---

## 7. Event Types

### 7.1 Event Definitions (events/types.go)

```go
package events

import "time"

// DeviceStateEvent carries device state for SSE subscribers and HAP updates.
type DeviceStateEvent struct {
    Timestamp       time.Time `json:"timestamp"`
    Source          string    `json:"source"`
    DeviceID        string    `json:"device_id"`
    Name            string    `json:"name"`

    // Sensor values
    Temperature     *float64  `json:"temperature,omitempty"`
    Humidity        *float64  `json:"humidity,omitempty"`
    Battery         *int      `json:"battery,omitempty"`
    Occupancy       *bool     `json:"occupancy,omitempty"`
    Illuminance     *int      `json:"illuminance,omitempty"`

    // Light values
    On              *bool     `json:"on,omitempty"`
    Brightness      *int      `json:"brightness,omitempty"`      // 0-100
    Hue             *float64  `json:"hue,omitempty"`             // 0-360
    Saturation      *float64  `json:"saturation,omitempty"`      // 0-100
    ColorTemp       *int      `json:"color_temp,omitempty"`      // mireds

    // Connectivity
    LinkQuality     int       `json:"link_quality"`
    LastSeen        time.Time `json:"last_seen"`
    LastUpdated     time.Time `json:"last_updated"`
    ConnectionState string    `json:"connection_state"`
}

// CommandType represents supported device commands.
type CommandType string

const (
    CommandTypeSetPower      CommandType = "set_power"
    CommandTypeSetBrightness CommandType = "set_brightness"
    CommandTypeSetColor      CommandType = "set_color"
    CommandTypeSetColorTemp  CommandType = "set_color_temp"
)

// CommandEvent captures requested control actions for a device.
type CommandEvent struct {
    Timestamp   time.Time   `json:"timestamp"`
    Source      string      `json:"source"`
    DeviceID    string      `json:"device_id"`
    CommandType CommandType `json:"command_type"`

    // Command payloads (only one set per event)
    On          *bool    `json:"on,omitempty"`
    Brightness  *int     `json:"brightness,omitempty"`
    Hue         *float64 `json:"hue,omitempty"`
    Saturation  *float64 `json:"saturation,omitempty"`
    ColorTemp   *int     `json:"color_temp,omitempty"`
}

// ConnectionStatusEvent conveys component lifecycle information.
type ConnectionStatusEvent struct {
    Timestamp  time.Time        `json:"timestamp"`
    Component  string           `json:"component"`
    Status     ConnectionStatus `json:"status"`
    Error      string           `json:"error"`
    Reconnects int              `json:"reconnects"`
}

type ConnectionStatus string

const (
    ConnectionStatusDisconnected ConnectionStatus = "disconnected"
    ConnectionStatusConnecting   ConnectionStatus = "connecting"
    ConnectionStatusConnected    ConnectionStatus = "connected"
    ConnectionStatusReconnecting ConnectionStatus = "reconnecting"
    ConnectionStatusFailed       ConnectionStatus = "failed"
)
```

---

## 8. Lightbulb Implementation Details (brutella/hap)

This section provides detailed implementation guidance for the various lightbulb types using the brutella/hap library.

### 8.1 HAP Lightbulb Types Overview

The brutella/hap library provides several lightbulb service types:

| Service Type | Characteristics | Use Case |
|--------------|-----------------|----------|
| `service.Lightbulb` | On | Simple on/off switch |
| `service.ColoredLightbulb` | On, Brightness, Hue, Saturation | Full color bulbs (RGB) |
| Custom (see below) | On, Brightness | Dimmable white bulbs |
| Custom (see below) | On, Brightness, ColorTemperature | Tunable white bulbs |

### 8.2 Characteristic Value Ranges

| Characteristic | HAP Range | Z2M Range | Conversion |
|----------------|-----------|-----------|------------|
| On | true/false | "ON"/"OFF" | string comparison |
| Brightness | 0-100 (%) | 0-254 | `hap = z2m * 100 / 254` |
| Hue | 0-360 (degrees) | 0-360 | Direct mapping |
| Saturation | 0-100 (%) | 0-100 | Direct mapping |
| ColorTemperature | 140-500 (mireds) | 150-500 (mireds) | Direct mapping (clamp to HAP range) |

### 8.3 Accessory Implementations

#### 8.3.1 Simple On/Off Lightbulb

```go
// devices/accessories/lightbulb.go

import (
    "github.com/brutella/hap/accessory"
    "github.com/brutella/hap/service"
)

// SimpleLightbulb - on/off only (no dimming)
type SimpleLightbulb struct {
    *accessory.A
    Lightbulb *service.Lightbulb
}

func NewSimpleLightbulb(info accessory.Info) *SimpleLightbulb {
    a := SimpleLightbulb{}
    a.A = accessory.New(info, accessory.TypeLightbulb)
    a.Lightbulb = service.NewLightbulb()
    a.AddS(a.Lightbulb.S)
    return &a
}

// Usage:
// a.Lightbulb.On.SetValue(true)
// a.Lightbulb.On.OnValueRemoteUpdate(func(on bool) { ... })
```

#### 8.3.2 Dimmable Lightbulb

```go
// DimmableLightbulb - on/off + brightness
type DimmableLightbulb struct {
    *accessory.A
    Lightbulb  *service.Lightbulb
    Brightness *characteristic.Brightness
}

func NewDimmableLightbulb(info accessory.Info) *DimmableLightbulb {
    a := DimmableLightbulb{}
    a.A = accessory.New(info, accessory.TypeLightbulb)

    a.Lightbulb = service.NewLightbulb()
    a.AddS(a.Lightbulb.S)

    // Add brightness characteristic to the lightbulb service
    a.Brightness = characteristic.NewBrightness()
    a.Lightbulb.AddC(a.Brightness.C)

    return &a
}

// Usage:
// a.Lightbulb.On.SetValue(true)
// a.Brightness.SetValue(75)  // 75%
// a.Brightness.OnValueRemoteUpdate(func(brightness int) {
//     // Convert to z2m: z2mBrightness := brightness * 254 / 100
// })
```

#### 8.3.3 Color Lightbulb (HSV)

```go
// Using the built-in ColoredLightbulb from brutella/hap

import (
    "github.com/brutella/hap/accessory"
)

func NewColorLightbulb(info accessory.Info) *accessory.ColoredLightbulb {
    return accessory.NewColoredLightbulb(info)
}

// The ColoredLightbulb includes:
// - Lightbulb.On         (bool)
// - Lightbulb.Brightness (int, 0-100)
// - Lightbulb.Hue        (float64, 0-360)
// - Lightbulb.Saturation (float64, 0-100)

// Usage:
// bulb := accessory.NewColoredLightbulb(info)
// bulb.Lightbulb.On.SetValue(true)
// bulb.Lightbulb.Brightness.SetValue(100)
// bulb.Lightbulb.Hue.SetValue(240.0)       // Blue
// bulb.Lightbulb.Saturation.SetValue(100.0)
//
// bulb.Lightbulb.Hue.OnValueRemoteUpdate(func(hue float64) {
//     // Send to z2m: {"color": {"hue": hue, "saturation": currentSat}}
// })
```

#### 8.3.4 Color Temperature Lightbulb

```go
// CTLightbulb - on/off + brightness + color temperature
type CTLightbulb struct {
    *accessory.A
    Lightbulb        *service.Lightbulb
    Brightness       *characteristic.Brightness
    ColorTemperature *characteristic.ColorTemperature
}

func NewCTLightbulb(info accessory.Info) *CTLightbulb {
    a := CTLightbulb{}
    a.A = accessory.New(info, accessory.TypeLightbulb)

    a.Lightbulb = service.NewLightbulb()
    a.AddS(a.Lightbulb.S)

    // Add brightness
    a.Brightness = characteristic.NewBrightness()
    a.Lightbulb.AddC(a.Brightness.C)

    // Add color temperature (140-500 mireds)
    // 140 mireds = ~7143K (cool/daylight)
    // 500 mireds = ~2000K (warm/candlelight)
    a.ColorTemperature = characteristic.NewColorTemperature()
    a.Lightbulb.AddC(a.ColorTemperature.C)

    return &a
}

// Usage:
// a.Lightbulb.On.SetValue(true)
// a.Brightness.SetValue(100)
// a.ColorTemperature.SetValue(370)  // Warm white
//
// a.ColorTemperature.OnValueRemoteUpdate(func(ct int) {
//     // Send to z2m: {"color_temp": ct}
// })
```

#### 8.3.5 Full-Featured Lightbulb (Color + CT)

Some bulbs support both HSV color AND color temperature. HomeKit handles the mode switching internally.

```go
// FullLightbulb - on/off + brightness + color + color temperature
type FullLightbulb struct {
    *accessory.A
    Lightbulb        *service.Lightbulb
    Brightness       *characteristic.Brightness
    Hue              *characteristic.Hue
    Saturation       *characteristic.Saturation
    ColorTemperature *characteristic.ColorTemperature
}

func NewFullLightbulb(info accessory.Info) *FullLightbulb {
    a := FullLightbulb{}
    a.A = accessory.New(info, accessory.TypeLightbulb)

    a.Lightbulb = service.NewLightbulb()
    a.AddS(a.Lightbulb.S)

    a.Brightness = characteristic.NewBrightness()
    a.Lightbulb.AddC(a.Brightness.C)

    a.Hue = characteristic.NewHue()
    a.Lightbulb.AddC(a.Hue.C)

    a.Saturation = characteristic.NewSaturation()
    a.Lightbulb.AddC(a.Saturation.C)

    a.ColorTemperature = characteristic.NewColorTemperature()
    a.Lightbulb.AddC(a.ColorTemperature.C)

    return &a
}
```

### 8.4 Switchable Interface for Unified Handling

Following the tasmota-homekit pattern, create a unified interface:

```go
// Controllable represents any accessory that can be controlled
type Controllable interface {
    SetOn(on bool)
    OnValue() bool
    OnValueRemoteUpdate(f func(on bool))
    ID() uint64
}

// Dimmable extends Controllable with brightness
type Dimmable interface {
    Controllable
    SetBrightness(brightness int)
    BrightnessValue() int
    BrightnessRemoteUpdate(f func(brightness int))
}

// Colorable extends Dimmable with color control
type Colorable interface {
    Dimmable
    SetHue(hue float64)
    SetSaturation(saturation float64)
    HueValue() float64
    SaturationValue() float64
    HueRemoteUpdate(f func(hue float64))
    SaturationRemoteUpdate(f func(saturation float64))
}

// CTAdjustable extends Dimmable with color temperature
type CTAdjustable interface {
    Dimmable
    SetColorTemperature(ct int)
    ColorTemperatureValue() int
    ColorTemperatureRemoteUpdate(f func(ct int))
}
```

### 8.5 Value Conversion Functions

```go
// conversion.go

// Z2M brightness (0-254) to HAP brightness (0-100)
func Z2MBrightnessToHAP(z2m int) int {
    if z2m <= 0 {
        return 0
    }
    if z2m >= 254 {
        return 100
    }
    return int(float64(z2m) * 100.0 / 254.0)
}

// HAP brightness (0-100) to Z2M brightness (0-254)
func HAPBrightnessToZ2M(hap int) int {
    if hap <= 0 {
        return 0
    }
    if hap >= 100 {
        return 254
    }
    return int(float64(hap) * 254.0 / 100.0)
}

// Clamp color temperature to HAP valid range (140-500)
func ClampColorTemp(ct int) int {
    if ct < 140 {
        return 140
    }
    if ct > 500 {
        return 500
    }
    return ct
}

// Z2M state string to bool
func Z2MStateToBool(state string) bool {
    return state == "ON"
}

// Bool to Z2M state string
func BoolToZ2MState(on bool) string {
    if on {
        return "ON"
    }
    return "OFF"
}
```

### 8.6 MQTT Command Payloads for Lights

```go
// LightCommand represents a command to send to zigbee2mqtt
type LightCommand struct {
    State      *string  `json:"state,omitempty"`
    Brightness *int     `json:"brightness,omitempty"`
    ColorTemp  *int     `json:"color_temp,omitempty"`
    Color      *Color   `json:"color,omitempty"`
    Transition *float64 `json:"transition,omitempty"` // seconds
}

type Color struct {
    Hue        *float64 `json:"hue,omitempty"`
    Saturation *float64 `json:"saturation,omitempty"`
    X          *float64 `json:"x,omitempty"`
    Y          *float64 `json:"y,omitempty"`
}

// Build command examples:

// Turn on
cmd := LightCommand{State: ptr("ON")}

// Set brightness (with state)
cmd := LightCommand{
    State:      ptr("ON"),
    Brightness: ptr(HAPBrightnessToZ2M(75)),
}

// Set color
cmd := LightCommand{
    Color: &Color{
        Hue:        ptr(240.0),
        Saturation: ptr(100.0),
    },
}

// Set color temperature
cmd := LightCommand{
    ColorTemp: ptr(370),
}

// With smooth transition (2 seconds)
cmd := LightCommand{
    State:      ptr("ON"),
    Brightness: ptr(254),
    Transition: ptr(2.0),
}
```

### 8.7 Accessory Factory

```go
// CreateAccessory creates the appropriate HomeKit accessory based on device config
func CreateAccessory(device Device) (Controllable, *accessory.A) {
    info := accessory.Info{
        Name:         device.Name,
        Manufacturer: "Zigbee2MQTT",
        Model:        string(device.Type),
        SerialNumber: device.ID,
    }

    switch device.Type {
    case DeviceTypeLightbulb:
        return createLightbulbAccessory(info, device.Features)
    case DeviceTypeClimateSensor:
        // Returns nil Controllable since sensors aren't controllable
        return nil, createClimateSensorAccessory(info, device.Features)
    case DeviceTypeOccupancySensor:
        return nil, createOccupancySensorAccessory(info, device.Features)
    default:
        return nil, nil
    }
}

func createLightbulbAccessory(info accessory.Info, features DeviceFeatures) (Controllable, *accessory.A) {
    // Determine bulb type based on features
    hasColor := features.Color
    hasCT := features.ColorTemperature
    hasBrightness := features.Brightness

    switch {
    case hasColor && hasCT:
        // Full-featured bulb
        bulb := NewFullLightbulb(info)
        return &FullLightbulbWrapper{bulb}, bulb.A

    case hasColor:
        // Color bulb (use built-in ColoredLightbulb)
        bulb := accessory.NewColoredLightbulb(info)
        return &ColoredLightbulbWrapper{bulb}, bulb.A

    case hasCT:
        // Color temperature bulb
        bulb := NewCTLightbulb(info)
        return &CTLightbulbWrapper{bulb}, bulb.A

    case hasBrightness:
        // Dimmable bulb
        bulb := NewDimmableLightbulb(info)
        return &DimmableLightbulbWrapper{bulb}, bulb.A

    default:
        // Simple on/off
        bulb := NewSimpleLightbulb(info)
        return &SimpleLightbulbWrapper{bulb}, bulb.A
    }
}
```

### 8.8 State Update Flow for Lights

```go
// UpdateLightState updates the HomeKit accessory from a Z2M state message
func (hm *HAPManager) UpdateLightState(deviceID string, state *LightState) {
    acc, exists := hm.accessories[deviceID]
    if !exists {
        return
    }

    // Update On state
    if state.State != nil {
        acc.SetOn(Z2MStateToBool(*state.State))
    }

    // Update Brightness if dimmable
    if dimmable, ok := acc.(Dimmable); ok && state.Brightness != nil {
        dimmable.SetBrightness(Z2MBrightnessToHAP(*state.Brightness))
    }

    // Update Color if colorable
    if colorable, ok := acc.(Colorable); ok && state.Color != nil {
        if state.Color.Hue != nil {
            colorable.SetHue(*state.Color.Hue)
        }
        if state.Color.Saturation != nil {
            colorable.SetSaturation(*state.Color.Saturation)
        }
    }

    // Update Color Temperature if CT-adjustable
    if ctAdj, ok := acc.(CTAdjustable); ok && state.ColorTemp != nil {
        ctAdj.SetColorTemperature(ClampColorTemp(*state.ColorTemp))
    }
}
```

---

## 9. Implementation Phases

### Phase 1: Foundation (Core Infrastructure)

1. **Project Setup**
   - Initialize Go module (`github.com/kradalby/z2m-homekit`)
   - Create `flake.nix` with devShell, apps, and package
   - Set up directory structure per DEV_GUIDE.md

2. **Configuration Package**
   - Implement `config/config.go` with go-env
   - Define all Z2M_HOMEKIT_* environment variables
   - Add validation and parsed helpers

3. **Logging Package**
   - Implement `logging/logger.go` with slog
   - Support json and console formats

4. **Event Bus**
   - Implement `events/bus.go` wrapping tailscale eventbus
   - Define client names and event types

5. **Device Configuration**
   - Implement HuJSON config loading
   - Define device types and features

### Phase 2: MQTT Integration

1. **Embedded MQTT Broker**
   - Set up mochi-mqtt server
   - Implement MQTTHook for message handling
   - Parse zigbee2mqtt message formats

2. **Device Manager**
   - Implement state tracking per device
   - Subscribe to MQTT state updates
   - Publish commands to MQTT

3. **Message Parsing**
   - Handle sensor messages (temp/humidity/battery/occupancy)
   - Handle light messages (state/brightness/color)
   - Extract link quality and availability

### Phase 3: HomeKit Integration

1. **HAP Manager**
   - Create bridge accessory
   - Implement accessory factory based on device type

2. **Sensor Accessories**
   - TemperatureSensor with CurrentTemperature
   - HumiditySensor with CurrentRelativeHumidity
   - OccupancySensor with OccupancyDetected, BatteryLevel

3. **Light Accessories**
   - Lightbulb with On, Brightness
   - Color support (Hue, Saturation)
   - ColorTemperature support

4. **State Synchronization**
   - Subscribe to DeviceStateEvent
   - Update accessory characteristics
   - Handle OnValueRemoteUpdate callbacks

### Phase 4: Web UI

1. **KraWeb Integration**
   - Set up kra/web server
   - Configure Tailscale integration

2. **Dashboard**
   - Device grid with elem-go
   - Real-time updates via SSE
   - Control buttons for lights

3. **HomeKit Pairing**
   - QR code display (homekit-qr)
   - PIN display
   - Pairing instructions

4. **Debug Endpoints**
   - /metrics for Prometheus
   - /debug/* via tsweb
   - /health endpoint

### Phase 5: Testing & Hardening

1. **Unit Tests**
   - Config validation tests
   - Device type parsing tests
   - Message parsing tests
   - HAP characteristic conversion tests

2. **Integration Tests**
   - MQTT message flow
   - Event bus propagation
   - HAP state synchronization

3. **NixOS Module**
   - Complete module.nix
   - VM test (nix/test.nix)

---

## 9A. Comprehensive Testing Strategy

This section details the thorough testing approach for every component and state transition.

### 9A.1 Test File Structure

```
z2m-homekit/
├── config/
│   └── config_test.go           # Configuration loading and validation
├── devices/
│   ├── types_test.go            # Device type parsing
│   ├── manager_test.go          # Device manager state handling
│   └── accessories/
│       ├── lightbulb_test.go    # All lightbulb variants
│       ├── sensor_test.go       # Temperature, humidity, occupancy
│       └── conversion_test.go   # Value conversion functions
├── events/
│   ├── bus_test.go              # Event bus pub/sub and deduplication
│   └── types_test.go            # Event serialization
├── app_test.go                  # Application lifecycle
├── mqtt_test.go                 # MQTT message parsing
├── hap_test.go                  # HomeKit accessory management
├── web_test.go                  # Web handlers and SSE
├── state_sync_test.go           # End-to-end state synchronization
├── main_test.go                 # Integration tests
└── nix/
    └── test.nix                 # NixOS VM tests
```

### 9A.2 Configuration Tests (config/config_test.go)

```go
func TestConfig_Load(t *testing.T) {
    tests := []struct {
        name    string
        env     map[string]string
        wantErr bool
    }{
        {
            name: "defaults",
            env:  map[string]string{},
            wantErr: false,
        },
        {
            name: "custom HAP pin",
            env:  map[string]string{"Z2M_HOMEKIT_HAP_PIN": "12345678"},
            wantErr: false,
        },
        {
            name: "invalid HAP pin length",
            env:  map[string]string{"Z2M_HOMEKIT_HAP_PIN": "123"},
            wantErr: true,
        },
        {
            name: "invalid port",
            env:  map[string]string{"Z2M_HOMEKIT_HAP_PORT": "99999"},
            wantErr: true,
        },
        {
            name: "invalid log level",
            env:  map[string]string{"Z2M_HOMEKIT_LOG_LEVEL": "invalid"},
            wantErr: true,
        },
    }
    // ... test implementation
}

func TestConfig_HAPAddrPort(t *testing.T)
func TestConfig_WebAddrPort(t *testing.T)
func TestConfig_MQTTAddrPort(t *testing.T)
func TestConfig_applyNameDefaults(t *testing.T)
```

### 9A.3 Device Configuration Tests (devices/types_test.go)

```go
func TestLoadConfig_ValidHuJSON(t *testing.T)
func TestLoadConfig_InvalidHuJSON(t *testing.T)
func TestLoadConfig_MissingRequiredFields(t *testing.T)
func TestLoadConfig_DuplicateIDs(t *testing.T)
func TestLoadConfig_DefaultValues(t *testing.T)

func TestDevice_Validation(t *testing.T) {
    tests := []struct {
        name    string
        device  Device
        wantErr bool
    }{
        {
            name: "valid climate sensor",
            device: Device{
                ID:   "sensor-1",
                Name: "Living Room",
                Topic: "living-room-sensor",
                Type: DeviceTypeClimateSensor,
                Features: DeviceFeatures{Temperature: true, Humidity: true},
            },
            wantErr: false,
        },
        {
            name: "missing ID",
            device: Device{Name: "Test", Topic: "test", Type: DeviceTypeLightbulb},
            wantErr: true,
        },
        {
            name: "missing topic",
            device: Device{ID: "test", Name: "Test", Type: DeviceTypeLightbulb},
            wantErr: true,
        },
        // ... more cases
    }
}
```

### 9A.4 Value Conversion Tests (devices/accessories/conversion_test.go)

```go
func TestZ2MBrightnessToHAP(t *testing.T) {
    tests := []struct {
        z2m  int
        want int
    }{
        {0, 0},
        {1, 0},      // rounds down
        {127, 50},
        {254, 100},
        {255, 100},  // clamp
        {-1, 0},     // clamp
    }
    for _, tt := range tests {
        got := Z2MBrightnessToHAP(tt.z2m)
        assert.Equal(t, tt.want, got)
    }
}

func TestHAPBrightnessToZ2M(t *testing.T) {
    tests := []struct {
        hap  int
        want int
    }{
        {0, 0},
        {50, 127},
        {100, 254},
        {101, 254},  // clamp
        {-1, 0},     // clamp
    }
    // ...
}

func TestClampColorTemp(t *testing.T) {
    tests := []struct {
        input int
        want  int
    }{
        {100, 140},   // below min
        {140, 140},   // at min
        {300, 300},   // in range
        {500, 500},   // at max
        {600, 500},   // above max
    }
    // ...
}

func TestZ2MStateToBool(t *testing.T) {
    assert.True(t, Z2MStateToBool("ON"))
    assert.False(t, Z2MStateToBool("OFF"))
    assert.False(t, Z2MStateToBool(""))
    assert.False(t, Z2MStateToBool("on"))  // case sensitive
}

func TestBoolToZ2MState(t *testing.T) {
    assert.Equal(t, "ON", BoolToZ2MState(true))
    assert.Equal(t, "OFF", BoolToZ2MState(false))
}
```

### 9A.5 Lightbulb Accessory Tests (devices/accessories/lightbulb_test.go)

```go
func TestSimpleLightbulb(t *testing.T) {
    info := accessory.Info{Name: "Test Light"}
    bulb := NewSimpleLightbulb(info)

    assert.NotNil(t, bulb)
    assert.NotNil(t, bulb.Lightbulb)

    // Test initial state
    assert.False(t, bulb.Lightbulb.On.Value())

    // Test state changes
    bulb.Lightbulb.On.SetValue(true)
    assert.True(t, bulb.Lightbulb.On.Value())
}

func TestDimmableLightbulb(t *testing.T) {
    info := accessory.Info{Name: "Dimmable Light"}
    bulb := NewDimmableLightbulb(info)

    // Test On
    bulb.Lightbulb.On.SetValue(true)
    assert.True(t, bulb.Lightbulb.On.Value())

    // Test Brightness
    bulb.Brightness.SetValue(75)
    assert.Equal(t, 75, bulb.Brightness.Value())

    // Test brightness bounds
    bulb.Brightness.SetValue(0)
    assert.Equal(t, 0, bulb.Brightness.Value())

    bulb.Brightness.SetValue(100)
    assert.Equal(t, 100, bulb.Brightness.Value())
}

func TestColorLightbulb(t *testing.T) {
    info := accessory.Info{Name: "Color Light"}
    bulb := accessory.NewColoredLightbulb(info)

    // Test Hue
    bulb.Lightbulb.Hue.SetValue(240.0)
    assert.Equal(t, 240.0, bulb.Lightbulb.Hue.Value())

    // Test Saturation
    bulb.Lightbulb.Saturation.SetValue(100.0)
    assert.Equal(t, 100.0, bulb.Lightbulb.Saturation.Value())

    // Test Brightness
    bulb.Lightbulb.Brightness.SetValue(50)
    assert.Equal(t, 50, bulb.Lightbulb.Brightness.Value())
}

func TestCTLightbulb(t *testing.T) {
    info := accessory.Info{Name: "CT Light"}
    bulb := NewCTLightbulb(info)

    // Test ColorTemperature range
    bulb.ColorTemperature.SetValue(140)  // cool
    assert.Equal(t, 140, bulb.ColorTemperature.Value())

    bulb.ColorTemperature.SetValue(500)  // warm
    assert.Equal(t, 500, bulb.ColorTemperature.Value())
}

func TestFullLightbulb(t *testing.T) {
    info := accessory.Info{Name: "Full Light"}
    bulb := NewFullLightbulb(info)

    // Test all characteristics exist
    assert.NotNil(t, bulb.Brightness)
    assert.NotNil(t, bulb.Hue)
    assert.NotNil(t, bulb.Saturation)
    assert.NotNil(t, bulb.ColorTemperature)
}

func TestAccessoryFactory(t *testing.T) {
    tests := []struct {
        name     string
        device   Device
        wantType string
    }{
        {
            name: "simple lightbulb",
            device: Device{
                ID: "l1", Name: "Light", Topic: "l1", Type: DeviceTypeLightbulb,
                Features: DeviceFeatures{},
            },
            wantType: "*SimpleLightbulbWrapper",
        },
        {
            name: "dimmable lightbulb",
            device: Device{
                ID: "l2", Name: "Light", Topic: "l2", Type: DeviceTypeLightbulb,
                Features: DeviceFeatures{Brightness: true},
            },
            wantType: "*DimmableLightbulbWrapper",
        },
        {
            name: "color lightbulb",
            device: Device{
                ID: "l3", Name: "Light", Topic: "l3", Type: DeviceTypeLightbulb,
                Features: DeviceFeatures{Brightness: true, Color: true},
            },
            wantType: "*ColoredLightbulbWrapper",
        },
        {
            name: "CT lightbulb",
            device: Device{
                ID: "l4", Name: "Light", Topic: "l4", Type: DeviceTypeLightbulb,
                Features: DeviceFeatures{Brightness: true, ColorTemperature: true},
            },
            wantType: "*CTLightbulbWrapper",
        },
        {
            name: "full lightbulb",
            device: Device{
                ID: "l5", Name: "Light", Topic: "l5", Type: DeviceTypeLightbulb,
                Features: DeviceFeatures{Brightness: true, Color: true, ColorTemperature: true},
            },
            wantType: "*FullLightbulbWrapper",
        },
    }
    // ...
}
```

### 9A.6 Event Bus Tests (events/bus_test.go)

```go
func TestBus_New(t *testing.T) {
    logger := slog.Default()
    bus, err := New(logger)
    require.NoError(t, err)
    defer bus.Close()

    // Verify all clients created
    for _, name := range []ClientName{ClientDeviceManager, ClientHAP, ClientWeb, ClientMQTT, ClientMetrics} {
        client, err := bus.Client(name)
        assert.NoError(t, err)
        assert.NotNil(t, client)
    }
}

func TestBus_PublishStateUpdate(t *testing.T) {
    logger := slog.Default()
    bus, err := New(logger)
    require.NoError(t, err)
    defer bus.Close()

    client, _ := bus.Client(ClientMQTT)

    event := StateUpdateEvent{
        DeviceID:    "test-device",
        Name:        "Test",
        On:          ptr(true),
        Brightness:  ptr(75),
        LastUpdated: time.Now(),
    }

    // Should not panic
    bus.PublishStateUpdate(client, event)
}

func TestBus_Deduplication(t *testing.T) {
    logger := slog.Default()
    bus, err := New(logger)
    require.NoError(t, err)
    defer bus.Close()

    client, _ := bus.Client(ClientMQTT)
    subscriber := eventbus.Subscribe[StateUpdateEvent](client)

    event := StateUpdateEvent{
        DeviceID:    "test-device",
        Name:        "Test",
        On:          ptr(true),
        LastUpdated: time.Now(),
    }

    // Publish same event twice
    bus.PublishStateUpdate(client, event)
    bus.PublishStateUpdate(client, event)

    // Should only receive one event (deduplication)
    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()

    count := 0
    for {
        select {
        case <-subscriber.Events():
            count++
        case <-ctx.Done():
            assert.Equal(t, 1, count, "should deduplicate identical events")
            return
        }
    }
}

func TestBus_DifferentEventsNotDeduplicated(t *testing.T) {
    // Similar to above but with different events
    // Should receive both
}
```

### 9A.7 MQTT Hook Tests (mqtt_test.go)

```go
func TestMQTTHook_ParseZ2MClimateSensor(t *testing.T) {
    payload := `{"temperature":22.5,"humidity":58.42,"battery":97,"linkquality":142}`

    hook := &MQTTHook{/* mock publisher */}
    state, fields := hook.parsePayload("zigbee2mqtt/living-room-sensor", []byte(payload))

    assert.NotNil(t, state)
    assert.Equal(t, 22.5, *state.Temperature)
    assert.Equal(t, 58.42, *state.Humidity)
    assert.Equal(t, 97, *state.Battery)
    assert.Contains(t, fields, "Temperature")
    assert.Contains(t, fields, "Humidity")
    assert.Contains(t, fields, "Battery")
}

func TestMQTTHook_ParseZ2MLightbulb(t *testing.T) {
    tests := []struct {
        name    string
        payload string
        wantOn  bool
        wantBri int
        wantCT  int
    }{
        {
            name:    "light on with brightness",
            payload: `{"state":"ON","brightness":200,"linkquality":92}`,
            wantOn:  true,
            wantBri: 200,
        },
        {
            name:    "light off",
            payload: `{"state":"OFF","brightness":0,"linkquality":92}`,
            wantOn:  false,
            wantBri: 0,
        },
        {
            name:    "light with color temp",
            payload: `{"state":"ON","brightness":254,"color_temp":370,"color_mode":"color_temp"}`,
            wantOn:  true,
            wantBri: 254,
            wantCT:  370,
        },
    }
    // ...
}

func TestMQTTHook_ParseZ2MColorLight(t *testing.T) {
    payload := `{"state":"ON","brightness":254,"color":{"hue":240,"saturation":100},"color_mode":"hs"}`

    hook := &MQTTHook{/* mock publisher */}
    state, _ := hook.parsePayload("zigbee2mqtt/color-light", []byte(payload))

    assert.True(t, *state.On)
    assert.Equal(t, 254, *state.Brightness)
    assert.Equal(t, 240.0, *state.Color.Hue)
    assert.Equal(t, 100.0, *state.Color.Saturation)
}

func TestMQTTHook_IgnoresBridgeMessages(t *testing.T) {
    hook := &MQTTHook{/* mock publisher */}

    // Should ignore bridge topics
    state, _ := hook.parsePayload("zigbee2mqtt/bridge/state", []byte(`{"state":"online"}`))
    assert.Nil(t, state)

    state, _ = hook.parsePayload("zigbee2mqtt/bridge/devices", []byte(`[]`))
    assert.Nil(t, state)
}

func TestMQTTHook_IgnoresSetCommands(t *testing.T) {
    hook := &MQTTHook{/* mock publisher */}

    state, _ := hook.parsePayload("zigbee2mqtt/light/set", []byte(`{"state":"ON"}`))
    assert.Nil(t, state)
}

func TestMQTTHook_InvalidJSON(t *testing.T) {
    hook := &MQTTHook{/* mock publisher */}

    state, _ := hook.parsePayload("zigbee2mqtt/sensor", []byte(`{invalid json`))
    assert.Nil(t, state)
}
```

### 9A.8 HAP Manager Tests (hap_test.go)

```go
func TestHAPManager_CreateAccessories(t *testing.T) {
    devices := []Device{
        {ID: "l1", Name: "Light 1", Topic: "l1", Type: DeviceTypeLightbulb, Features: DeviceFeatures{Brightness: true}},
        {ID: "s1", Name: "Sensor 1", Topic: "s1", Type: DeviceTypeClimateSensor, Features: DeviceFeatures{Temperature: true}},
    }

    hm := NewHAPManager(devices, "Test Bridge", nil, nil, nil)
    accessories := hm.GetAccessories()

    // Should have bridge + 2 devices
    assert.Len(t, accessories, 3)
}

func TestHAPManager_UpdateState_Light(t *testing.T) {
    devices := []Device{
        {ID: "l1", Name: "Light", Topic: "l1", Type: DeviceTypeLightbulb, Features: DeviceFeatures{Brightness: true}},
    }

    hm := NewHAPManager(devices, "Test Bridge", nil, nil, nil)

    // Update state
    event := events.StateUpdateEvent{
        DeviceID:   "l1",
        On:         ptr(true),
        Brightness: ptr(75),  // HAP scale (0-100)
    }

    hm.UpdateState(event)

    // Verify accessory updated
    acc := hm.accessories["l1"]
    assert.True(t, acc.OnValue())
    if dimmable, ok := acc.(Dimmable); ok {
        assert.Equal(t, 75, dimmable.BrightnessValue())
    }
}

func TestHAPManager_UpdateState_Sensor(t *testing.T) {
    // Test temperature/humidity sensor updates
}

func TestHAPManager_CommandCallback(t *testing.T) {
    commands := make(chan devices.CommandEvent, 10)
    devices := []Device{
        {ID: "l1", Name: "Light", Topic: "l1", Type: DeviceTypeLightbulb},
    }

    hm := NewHAPManager(devices, "Test Bridge", commands, nil, nil)

    // Simulate HomeKit toggle
    acc := hm.accessories["l1"]
    acc.SetOn(true)

    // Should receive command
    select {
    case cmd := <-commands:
        assert.Equal(t, "l1", cmd.DeviceID)
        assert.True(t, *cmd.On)
    case <-time.After(time.Second):
        t.Fatal("expected command event")
    }
}

func TestHAPManager_SkipsDisabledDevices(t *testing.T) {
    disabled := false
    devices := []Device{
        {ID: "l1", Name: "Light", Topic: "l1", Type: DeviceTypeLightbulb, HomeKit: &disabled},
    }

    hm := NewHAPManager(devices, "Test Bridge", nil, nil, nil)
    accessories := hm.GetAccessories()

    // Only bridge, no light
    assert.Len(t, accessories, 1)
}

func TestHAPManager_StableAccessoryIDs(t *testing.T) {
    devices := []Device{
        {ID: "device-unique-id", Name: "Light", Topic: "x", Type: DeviceTypeLightbulb},
    }

    hm1 := NewHAPManager(devices, "Test", nil, nil, nil)
    hm2 := NewHAPManager(devices, "Test", nil, nil, nil)

    // IDs should be identical across instances
    assert.Equal(t, hm1.accessories["device-unique-id"].ID(), hm2.accessories["device-unique-id"].ID())
}
```

### 9A.9 State Synchronization Tests (state_sync_test.go)

```go
func TestStateSyncFlow_MQTTToHomeKit(t *testing.T) {
    // Setup full pipeline
    eventBus, _ := events.New(slog.Default())
    defer eventBus.Close()

    devices := []Device{{ID: "l1", Name: "Light", Topic: "l1", Type: DeviceTypeLightbulb}}
    commands := make(chan devices.CommandEvent, 10)

    deviceManager, _ := devices.NewManager(devices, commands, eventBus)
    hapManager := NewHAPManager(devices, "Test", commands, deviceManager, eventBus)

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    go deviceManager.ProcessStateEvents(ctx)
    hapManager.Start(ctx)

    // Simulate MQTT message
    mqttClient, _ := eventBus.Client(events.ClientMQTT)
    publisher := eventbus.Publish[devices.StateChangedEvent](mqttClient)

    publisher.Publish(devices.StateChangedEvent{
        DeviceID: "l1",
        State: devices.State{
            On:          true,
            Brightness:  200,
            LastUpdated: time.Now(),
        },
        UpdatedFields: []string{"On", "Brightness"},
    })

    // Wait for propagation
    time.Sleep(100 * time.Millisecond)

    // Verify HomeKit state updated
    acc := hapManager.accessories["l1"]
    assert.True(t, acc.OnValue())
}

func TestStateSyncFlow_HomeKitToMQTT(t *testing.T) {
    // Setup with mock MQTT publisher
    // Trigger HomeKit command
    // Verify MQTT message sent
}

func TestStateSyncFlow_RapidUpdates(t *testing.T) {
    // Test handling of rapid state changes
    // Verify deduplication works
    // Verify final state is correct
}

func TestStateSyncFlow_ConcurrentAccess(t *testing.T) {
    // Test thread safety with concurrent reads/writes
}
```

### 9A.10 Web Handler Tests (web_test.go)

```go
func TestWebServer_HandleIndex(t *testing.T) {
    // Setup
    req := httptest.NewRequest("GET", "/", nil)
    w := httptest.NewRecorder()

    webServer.HandleIndex(w, req)

    assert.Equal(t, http.StatusOK, w.Code)
    assert.Contains(t, w.Body.String(), "z2m-homekit")
}

func TestWebServer_HandleToggle(t *testing.T) {
    commands := make(chan devices.CommandEvent, 10)
    // Setup webServer with commands channel

    req := httptest.NewRequest("POST", "/toggle/l1", nil)
    w := httptest.NewRecorder()

    webServer.HandleToggle(w, req)

    assert.Equal(t, http.StatusOK, w.Code)

    // Verify command sent
    select {
    case cmd := <-commands:
        assert.Equal(t, "l1", cmd.DeviceID)
    case <-time.After(time.Second):
        t.Fatal("expected command")
    }
}

func TestWebServer_HandleSSE(t *testing.T) {
    // Test SSE connection
    // Verify events are streamed
}

func TestWebServer_HandleHealth(t *testing.T) {
    req := httptest.NewRequest("GET", "/health", nil)
    w := httptest.NewRecorder()

    webServer.HandleHealth(w, req)

    assert.Equal(t, http.StatusOK, w.Code)
}
```

### 9A.11 NixOS VM Test (nix/test.nix)

```nix
{ pkgs, system, self }:

pkgs.nixosTest {
  name = "z2m-homekit";

  nodes.machine = { config, pkgs, ... }: {
    imports = [ self.nixosModules.default ];

    services.z2m-homekit = {
      enable = true;
      package = self.packages.${system}.default;
      devicesConfig = pkgs.writeText "devices.hujson" ''
        {
          "devices": [
            {
              "id": "test-sensor",
              "name": "Test Sensor",
              "topic": "test-sensor",
              "type": "climate_sensor",
              "features": {"temperature": true}
            }
          ]
        }
      '';
      hap.pin = "00102003";
      ports.hap = 51826;
      ports.web = 8081;
      ports.mqtt = 1883;
    };
  };

  testScript = ''
    machine.wait_for_unit("z2m-homekit.service")
    machine.wait_for_open_port(51826)
    machine.wait_for_open_port(8081)
    machine.wait_for_open_port(1883)

    # Test web UI
    machine.succeed("curl -sf http://localhost:8081/ | grep -q 'z2m-homekit'")

    # Test health endpoint
    machine.succeed("curl -sf http://localhost:8081/health")

    # Test MQTT broker is accepting connections
    machine.succeed("timeout 5 ${pkgs.mosquitto}/bin/mosquitto_pub -h localhost -p 1883 -t test -m test")

    # Verify HAP is responding (mDNS)
    machine.succeed("systemctl is-active z2m-homekit.service")
  '';
}
```

### 9A.12 Test Helpers

```go
// testutil/helpers.go

func ptr[T any](v T) *T {
    return &v
}

func NewTestEventBus(t *testing.T) *events.Bus {
    t.Helper()
    logger := slog.New(slog.NewTextHandler(io.Discard, nil))
    bus, err := events.New(logger)
    require.NoError(t, err)
    t.Cleanup(func() { bus.Close() })
    return bus
}

func NewTestHAPManager(t *testing.T, devices []Device) *HAPManager {
    t.Helper()
    commands := make(chan devices.CommandEvent, 10)
    bus := NewTestEventBus(t)
    return NewHAPManager(devices, "Test Bridge", commands, nil, bus)
}

// MockMQTTPublisher for testing command publishing
type MockMQTTPublisher struct {
    Published []struct {
        Topic   string
        Payload []byte
    }
    mu sync.Mutex
}

func (m *MockMQTTPublisher) Publish(topic string, payload []byte) error {
    m.mu.Lock()
    defer m.mu.Unlock()
    m.Published = append(m.Published, struct {
        Topic   string
        Payload []byte
    }{topic, payload})
    return nil
}
```

### Phase 6: Documentation & Polish

1. **README**
   - Installation instructions
   - Configuration guide
   - Migration from homebridge

2. **Example Configurations**
   - Sample devices.hujson
   - NixOS deployment example

---

## 9. Required Libraries

```go
// go.mod

require (
    // Configuration
    github.com/Netflix/go-env v0.1.2

    // HomeKit
    github.com/brutella/hap v0.0.35

    // HTML Generation
    github.com/chasefleming/elem-go v0.31.0

    // HomeKit QR Code
    github.com/kradalby/homekit-qr v0.0.0-20251117145710-0ea350a04eaa

    // HTTP + Tailscale
    github.com/kradalby/kra v0.0.0-20251123203901-fcb00e81f17f

    // Embedded MQTT
    github.com/mochi-mqtt/server/v2 v2.7.9

    // Metrics
    github.com/prometheus/client_golang v1.23.2

    // Testing
    github.com/stretchr/testify v1.11.1

    // HuJSON config
    github.com/tailscale/hujson v0.0.0-20250605163823-992244df8c5a

    // Event bus and utilities
    tailscale.com v1.90.6
)
```

---

## 10. Migration Path from Homebridge

### 10.1 Current Homebridge Setup

- Service: `services.homebridges.mqttthing`
- Port: 51785 (HAP), 56815 (UI)
- PIN: 033-44-255
- MQTT: External broker at 127.0.0.1:1883

### 10.2 Migration Steps

1. **Export Device List**
   - Convert homebridge.nix accessories to devices.hujson
   - Map mqttthing types to z2m-homekit types

2. **Configure z2m-homekit**
   - Set matching HAP PIN for seamless re-pairing
   - Configure embedded MQTT broker

3. **Update zigbee2mqtt**
   - Point to embedded MQTT broker (127.0.0.1:1883)

4. **Disable Homebridge**
   - Stop homebridge service
   - Remove from Home.app if necessary

5. **Enable z2m-homekit**
   - Start service
   - Pair with Home.app using existing PIN

### 10.3 Device Mapping Reference

| Homebridge mqttthing | z2m-homekit |
|---------------------|-------------|
| humiditySensor | climate_sensor (humidity: true) |
| temperatureSensor | climate_sensor (temperature: true) |
| occupancySensor | occupancy_sensor |
| lightbulb (basic) | lightbulb (brightness: true) |
| lightbulb (color) | lightbulb (brightness: true, color: true) |
| lightbulb (CT) | lightbulb (brightness: true, color_temperature: true) |

---

## 11. Success Criteria

1. **Functional Requirements**
   - All current devices accessible in Home.app
   - Real-time state updates from zigbee2mqtt
   - Control commands sent correctly
   - Web UI displays all devices

2. **Non-Functional Requirements**
   - Service starts within 5 seconds
   - State updates propagate within 100ms
   - Memory usage < 50MB
   - Zero external dependencies (embedded MQTT)

3. **Operational Requirements**
   - NixOS module works correctly
   - Tailscale integration functional
   - Prometheus metrics exposed
   - Structured logging

---

## 12. Open Questions / Decisions Needed

1. **Device Discovery**
   - Should we support auto-discovery from zigbee2mqtt/bridge/devices?
   - Or require manual configuration in devices.hujson?

2. **Multi-room Support**
   - Should devices be grouped by room in config?
   - How to handle HomeKit room assignments?

3. **Availability Tracking**
   - How to handle zigbee2mqtt availability messages?
   - Should unavailable devices show "No Response" in HomeKit?

4. **Additional Device Types**
   - Contact sensors (door/window)?
   - Buttons/remotes?
   - Thermostats?

---

## 13. Architecture Deep-Dive (from tasmota-homekit analysis)

This section captures patterns and implementations directly from the tasmota-homekit reference that should be replicated.

### 13.1 Application Bootstrap Pattern (app.go)

The main application follows this initialization order:

```go
func Main() {
    // 1. Load configuration from environment
    cfg, err := appconfig.Load()

    // 2. Initialize structured logger
    logger, err := logging.New(cfg.LogLevel, cfg.LogFormat)
    slog.SetDefault(logger)

    // 3. Load device configuration (HuJSON)
    deviceCfg, err := devices.LoadConfig(cfg.DevicesConfigPath)

    // 4. Create context with signal handling
    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer cancel()

    // 5. Initialize event bus
    eventBus, err := events.New(logger)
    defer eventBus.Close()

    // 6. Create command channel
    commands := make(chan devices.CommandEvent, 10)

    // 7. Initialize metrics collector
    metricsCollector, err := metrics.NewCollector(ctx, logger, eventBus, nil)
    defer metricsCollector.Close()

    // 8. Setup MQTT broker with hooks
    mqttServer := mqtt.New(&mqtt.Options{InlineClient: true})
    mqttServer.AddHook(new(auth.AllowHook), nil)
    mqttHook := &MQTTHook{statePublisher: eventbus.Publish[...](mqttClient)}
    mqttServer.AddHook(mqttHook, nil)
    // Add TCP listener and start serving

    // 9. Initialize device manager
    deviceManager, err := devices.NewManager(deviceCfg.Devices, commands, eventBus)
    go deviceManager.ProcessCommands(ctx)
    go deviceManager.ProcessStateEvents(ctx)

    // 10. Initialize HAP manager
    hapManager := NewHAPManager(deviceCfg.Devices, cfg.BridgeName, commands, deviceManager, eventBus)
    hapManager.Start(ctx)
    defer hapManager.Close()

    // 11. Create HAP server
    fsStore := hap.NewFsStore(cfg.HAPStoragePath)
    hapServer, err := hap.NewServer(fsStore, accessories[0], accessories[1:]...)
    hapServer.Pin = cfg.HAPPin
    hapServer.Addr = cfg.HAPAddrPort().String()
    // Start HAP server in goroutine

    // 12. Setup web server with kra
    kraWeb, err := web.NewServer(kraConfig, kraOpts...)
    webServer := NewWebServer(logger, deviceManager, eventBus, kraWeb, cfg.HAPPin, qrCode, hapManager)
    webServer.Start(ctx)
    defer webServer.Close()

    // 13. Register HTTP handlers
    kraWeb.Handle("/", ...)
    kraWeb.Handle("/toggle/", ...)
    kraWeb.Handle("/events", ...)  // SSE
    kraWeb.Handle("/health", ...)
    SetupDebugHandlers(kraWeb, hapManager)

    // 14. Wait for shutdown
    <-ctx.Done()
}
```

### 13.2 Event Bus Pattern (events/bus.go)

The event bus wraps tailscale's eventbus with named clients and deduplication:

```go
type ClientName string

const (
    ClientDeviceManager ClientName = "devicemanager"
    ClientHAP           ClientName = "hap"
    ClientWeb           ClientName = "web"
    ClientMQTT          ClientName = "mqtt"
    ClientMetrics       ClientName = "metrics"
)

type Bus struct {
    bus        *eventbus.Bus
    clients    map[ClientName]*eventbus.Client
    logger     *slog.Logger
    ctx        context.Context
    cancel     context.CancelFunc
    lastStates map[string]StateUpdateEvent  // For deduplication
    stateMu    sync.Mutex
    mu         sync.RWMutex
}

// Key methods:
func New(logger *slog.Logger) (*Bus, error)
func (b *Bus) Client(name ClientName) (*eventbus.Client, error)
func (b *Bus) PublishStateUpdate(client *eventbus.Client, event StateUpdateEvent)
func (b *Bus) PublishCommand(client *eventbus.Client, event CommandEvent)
func (b *Bus) PublishConnectionStatus(client *eventbus.Client, event ConnectionStatusEvent)
func (b *Bus) Close() error
```

**Deduplication**: The bus tracks last states per device and skips publishing duplicate events.

### 13.3 MQTT Hook Pattern (mqtt.go)

The MQTT hook intercepts messages from the embedded broker:

```go
type MQTTHook struct {
    mqtt.HookBase
    statePublisher *eventbus.Publisher[devices.StateChangedEvent]
}

func (h *MQTTHook) ID() string { return "z2m-mqtt-hook" }

func (h *MQTTHook) Provides(b byte) bool {
    return bytes.Contains([]byte{
        mqtt.OnConnect,
        mqtt.OnDisconnect,
        mqtt.OnPublish,
    }, []byte{b})
}

func (h *MQTTHook) OnPublish(cl *mqtt.Client, pk packets.Packet) (packets.Packet, error) {
    topic := pk.TopicName
    payload := pk.Payload

    // Parse zigbee2mqtt topic: zigbee2mqtt/<device-name>
    parts := strings.Split(topic, "/")
    if len(parts) < 2 || parts[0] != "zigbee2mqtt" {
        return pk, nil
    }
    deviceID := parts[1]

    // Skip bridge messages and /set commands
    if deviceID == "bridge" || strings.HasSuffix(topic, "/set") {
        return pk, nil
    }

    // Parse JSON payload
    var msg map[string]interface{}
    if err := json.Unmarshal(payload, &msg); err != nil {
        return pk, nil
    }

    // Build state from payload and publish to eventbus
    state := parseZ2MState(deviceID, msg)
    h.statePublisher.Publish(devices.StateChangedEvent{
        DeviceID:      deviceID,
        State:         state,
        UpdatedFields: detectChangedFields(msg),
    })

    return pk, nil
}
```

### 13.4 Device Manager Pattern (devices/manager.go)

```go
type Manager struct {
    devices          map[string]*Info
    states           map[string]*State
    mu               sync.RWMutex
    commands         chan CommandEvent
    statePublisher   *eventbus.Publisher[StateChangedEvent]
    stateSubscriber  *eventbus.Subscriber[StateChangedEvent]
    eventBus         *events.Bus
    stateEventClient *eventbus.Client
    mqttPublisher    MQTTPublisher  // For sending commands
}

// Key methods:
func NewManager(deviceConfigs []Device, commands chan CommandEvent, bus *events.Bus) (*Manager, error)
func (m *Manager) ProcessCommands(ctx context.Context)      // Handles HomeKit commands
func (m *Manager) ProcessStateEvents(ctx context.Context)   // Merges MQTT state updates
func (m *Manager) Snapshot() map[string]DeviceSnapshot      // For web UI
func (m *Manager) Device(id string) (Device, State, bool)   // Get single device
```

### 13.5 HAP Manager Pattern (hap.go)

```go
type HAPManager struct {
    bridge          *accessory.Bridge
    accessories     map[string]Controllable
    accessoryOrder  []string
    commands        chan devices.CommandEvent
    deviceManager   *devices.Manager
    stateSubscriber *eventbus.Subscriber[events.StateUpdateEvent]
    eventBus        *events.Bus
    eventClient     *eventbus.Client
    server          *hap.Server
    store           hap.Store

    // Stats
    incomingCommands atomic.Uint64
    outgoingUpdates  atomic.Uint64
    lastActivity     atomic.Int64
}

func NewHAPManager(...) *HAPManager {
    // Create bridge
    bridge := accessory.NewBridge(accessory.Info{...})

    // Create accessories for each device
    for _, device := range devices {
        if device.HomeKit != nil && !*device.HomeKit {
            continue  // Skip if disabled
        }

        acc := createAccessory(device)
        acc.A.Id = hashString(device.ID)  // Stable ID

        // Register command handler
        acc.OnValueRemoteUpdate(func(value interface{}) {
            commands <- devices.CommandEvent{DeviceID: device.ID, ...}
        })

        hm.accessories[device.ID] = acc
    }

    return hm
}

func (hm *HAPManager) ProcessStateChanges(ctx context.Context) {
    for {
        select {
        case event := <-hm.stateSubscriber.Events():
            hm.UpdateState(event)
        case <-ctx.Done():
            return
        }
    }
}
```

### 13.6 Metrics Collector Pattern (metrics/collector.go)

```go
type Collector struct {
    logger         *slog.Logger
    statusSub      *eventbus.Subscriber[events.ConnectionStatusEvent]
    commandSub     *eventbus.Subscriber[events.CommandEvent]
    statusGauge    *prometheus.GaugeVec
    commandCounter *prometheus.CounterVec
    ctx            context.Context
    cancel         context.CancelFunc
    workers        sync.WaitGroup
}

// Metrics exposed:
// - z2m_homekit_component_status{component, status} - gauge
// - z2m_homekit_command_total{source, device_id, command_type} - counter
// - z2m_homekit_device_state{device_id, ...} - gauge for each device property
```

### 13.7 Debug Handlers Pattern (debug.go)

```go
func SetupDebugHandlers(kraWeb interface{Handle(string, http.Handler)}, hapManager *HAPManager) {
    kraWeb.Handle("/debug/hap", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        debugInfo := hapManager.DebugInfo()
        json.NewEncoder(w).Encode(debugInfo)
    }))
}

type HAPDebugInfo struct {
    Server      *ServerInfo     `json:"server,omitempty"`
    Pairings    []PairingInfo   `json:"pairings,omitempty"`
    Stats       StatsInfo       `json:"stats"`
    Accessories []AccessoryInfo `json:"accessories"`
}
```

### 13.8 Config Package Pattern (config/config.go)

```go
type Config struct {
    // HAP listener
    HAPPin         string `env:"Z2M_HOMEKIT_HAP_PIN,default=00102003"`
    HAPStoragePath string `env:"Z2M_HOMEKIT_HAP_STORAGE_PATH,default=./data/hap"`
    HAPAddr        string `env:"Z2M_HOMEKIT_HAP_ADDR"`
    HAPBindAddress string `env:"Z2M_HOMEKIT_HAP_BIND_ADDRESS,default=0.0.0.0"`
    HAPPort        int    `env:"Z2M_HOMEKIT_HAP_PORT,default=51826"`

    // ... other fields

    // Parsed addresses (internal)
    hapAddr  netip.AddrPort
    webAddr  netip.AddrPort
    mqttAddr netip.AddrPort
}

func Load() (*Config, error) {
    var cfg Config
    if _, err := env.UnmarshalFromEnviron(&cfg); err != nil {
        return nil, err
    }
    cfg.applyNameDefaults()
    if err := cfg.Validate(); err != nil {
        return nil, err
    }
    return &cfg, nil
}

func (c *Config) HAPAddrPort() netip.AddrPort { return c.hapAddr }
```

---

## 14. MQTT-Thing Features Analysis

Key features from homebridge-mqttthing that should be supported:

### 14.1 Supported Accessory Types (from mqttthing)

| mqttthing Type | z2m-homekit Equivalent | Priority |
|----------------|------------------------|----------|
| `lightbulb` | `lightbulb` | High |
| `lightbulb-Dimmable` | `lightbulb` (brightness: true) | High |
| `lightbulb-ColTemp` | `lightbulb` (brightness: true, color_temperature: true) | High |
| `lightbulb-HSV` | `lightbulb` (brightness: true, color: true) | High |
| `lightbulb-RGB` | `lightbulb` (brightness: true, color: true) | Medium |
| `temperatureSensor` | `climate_sensor` (temperature: true) | High |
| `humiditySensor` | `climate_sensor` (humidity: true) | High |
| `occupancySensor` | `occupancy_sensor` | High |
| `motionSensor` | `occupancy_sensor` | High |
| `contactSensor` | `contact_sensor` | Medium |
| `outlet` | `outlet` | Medium |
| `switch` | `switch` | Medium |

### 14.2 Lightbulb Value Ranges (from mqttthing docs)

| Property | mqttthing Range | HomeKit Range | Notes |
|----------|-----------------|---------------|-------|
| Hue | 0-360 | 0-360 | Direct mapping |
| Saturation | 0-100 | 0-100 | Direct mapping |
| Brightness | 0-100 | 0-100 | HAP native |
| White | 0-255 | N/A | Convert to brightness |
| ColorTemperature | 140-500 | 140-500 | Mireds, configurable min/max |

### 14.3 JSON Codec Pattern

mqttthing uses a codec system for JSON path extraction. We implement this directly:

```go
// Z2M publishes: {"temperature": 22.5, "humidity": 58.42, "battery": 97}
// We extract each field directly from the JSON

type Z2MClimateSensorState struct {
    Temperature float64 `json:"temperature"`
    Humidity    float64 `json:"humidity"`
    Battery     int     `json:"battery"`
    LinkQuality int     `json:"linkquality"`
    Voltage     int     `json:"voltage,omitempty"`
    Pressure    float64 `json:"pressure,omitempty"`
}

type Z2MLightState struct {
    State      string  `json:"state"`          // "ON" or "OFF"
    Brightness int     `json:"brightness"`      // 0-254
    ColorTemp  int     `json:"color_temp"`      // 150-500 mireds
    ColorMode  string  `json:"color_mode"`      // "color_temp", "xy", "hs"
    Color      *Color  `json:"color,omitempty"`
    LinkQuality int    `json:"linkquality"`
}

type Color struct {
    Hue        float64 `json:"hue,omitempty"`        // 0-360
    Saturation float64 `json:"saturation,omitempty"` // 0-100
    X          float64 `json:"x,omitempty"`          // CIE x
    Y          float64 `json:"y,omitempty"`          // CIE y
}
```

### 14.4 Topic Patterns

```
# State topics (subscribe)
zigbee2mqtt/<device-name>           # Device state JSON
zigbee2mqtt/<device-name>/availability  # "online" or "offline"

# Command topics (publish)
zigbee2mqtt/<device-name>/set       # Send commands as JSON

# Bridge topics (informational)
zigbee2mqtt/bridge/state            # Bridge connection state
zigbee2mqtt/bridge/devices          # Device list (for discovery)
zigbee2mqtt/bridge/groups           # Group list
```

---

## 15. References

- [tasmota-homekit](https://github.com/kradalby/tasmota-homekit) - Reference implementation
- [brutella/hap](https://github.com/brutella/hap) - HomeKit library
- [mochi-mqtt](https://github.com/mochi-mqtt/server) - MQTT broker
- [zigbee2mqtt](https://www.zigbee2mqtt.io/) - Zigbee bridge
- [homebridge-mqttthing](https://github.com/arachnetech/homebridge-mqttthing) - Current solution
- [DEV_GUIDE.md](./DEV_GUIDE.md) - Development standards
