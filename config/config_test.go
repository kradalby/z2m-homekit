package config

import (
	"os"
	"testing"
)

func clearEnvVars() {
	envVars := []string{
		"Z2M_HOMEKIT_HAP_PIN",
		"Z2M_HOMEKIT_HAP_STORAGE_PATH",
		"Z2M_HOMEKIT_HAP_ADDR",
		"Z2M_HOMEKIT_HAP_BIND_ADDRESS",
		"Z2M_HOMEKIT_HAP_PORT",
		"Z2M_HOMEKIT_WEB_ADDR",
		"Z2M_HOMEKIT_WEB_BIND_ADDRESS",
		"Z2M_HOMEKIT_WEB_PORT",
		"Z2M_HOMEKIT_MQTT_ADDR",
		"Z2M_HOMEKIT_MQTT_BIND_ADDRESS",
		"Z2M_HOMEKIT_MQTT_PORT",
		"Z2M_HOMEKIT_DEVICES_CONFIG",
		"Z2M_HOMEKIT_LOG_LEVEL",
		"Z2M_HOMEKIT_LOG_FORMAT",
		"Z2M_HOMEKIT_TS_HOSTNAME",
		"Z2M_HOMEKIT_TS_STATE_DIR",
		"Z2M_HOMEKIT_TS_AUTHKEY",
		"Z2M_HOMEKIT_BRIDGE_NAME",
	}
	for _, env := range envVars {
		os.Unsetenv(env)
	}
}

func TestDefaultConfig(t *testing.T) {
	clearEnvVars()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Check defaults
	if cfg.HAPPin != "00102003" {
		t.Errorf("default HAPPin = %q, want %q", cfg.HAPPin, "00102003")
	}
	if cfg.HAPStoragePath != "./data/hap" {
		t.Errorf("default HAPStoragePath = %q, want %q", cfg.HAPStoragePath, "./data/hap")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("default LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
	if cfg.LogFormat != "json" {
		t.Errorf("default LogFormat = %q, want %q", cfg.LogFormat, "json")
	}
	if cfg.HAPPort != 51826 {
		t.Errorf("default HAPPort = %d, want %d", cfg.HAPPort, 51826)
	}
	if cfg.WebPort != 8081 {
		t.Errorf("default WebPort = %d, want %d", cfg.WebPort, 8081)
	}
	if cfg.MQTTPort != 1883 {
		t.Errorf("default MQTTPort = %d, want %d", cfg.MQTTPort, 1883)
	}
}

func TestConfigFromEnv(t *testing.T) {
	clearEnvVars()

	// Set custom values
	os.Setenv("Z2M_HOMEKIT_HAP_PIN", "12345678")
	os.Setenv("Z2M_HOMEKIT_HAP_ADDR", "127.0.0.1:51827")
	os.Setenv("Z2M_HOMEKIT_LOG_LEVEL", "debug")
	os.Setenv("Z2M_HOMEKIT_LOG_FORMAT", "console")
	defer clearEnvVars()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HAPPin != "12345678" {
		t.Errorf("HAPPin = %q, want %q", cfg.HAPPin, "12345678")
	}
	if cfg.HAPAddr != "127.0.0.1:51827" {
		t.Errorf("HAPAddr = %q, want %q", cfg.HAPAddr, "127.0.0.1:51827")
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
	if cfg.LogFormat != "console" {
		t.Errorf("LogFormat = %q, want %q", cfg.LogFormat, "console")
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		setup   func()
		wantErr bool
	}{
		{
			name: "invalid pin length",
			setup: func() {
				clearEnvVars()
				os.Setenv("Z2M_HOMEKIT_HAP_PIN", "123") // Too short
			},
			wantErr: true,
		},
		{
			name: "valid pin",
			setup: func() {
				clearEnvVars()
				os.Setenv("Z2M_HOMEKIT_HAP_PIN", "12345678")
			},
			wantErr: false,
		},
		{
			name: "invalid log level",
			setup: func() {
				clearEnvVars()
				os.Setenv("Z2M_HOMEKIT_LOG_LEVEL", "invalid")
			},
			wantErr: true,
		},
		{
			name: "invalid log format",
			setup: func() {
				clearEnvVars()
				os.Setenv("Z2M_HOMEKIT_LOG_FORMAT", "invalid")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()
			defer clearEnvVars()

			_, err := Load()
			if (err != nil) != tt.wantErr {
				t.Errorf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAddrPortMethods(t *testing.T) {
	clearEnvVars()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	hapAddr := cfg.HAPAddrPort()
	if !hapAddr.IsValid() {
		t.Error("HAPAddrPort() returned invalid address")
	}
	if hapAddr.Port() != 51826 {
		t.Errorf("HAPAddrPort().Port() = %d, want %d", hapAddr.Port(), 51826)
	}

	webAddr := cfg.WebAddrPort()
	if !webAddr.IsValid() {
		t.Error("WebAddrPort() returned invalid address")
	}
	if webAddr.Port() != 8081 {
		t.Errorf("WebAddrPort().Port() = %d, want %d", webAddr.Port(), 8081)
	}

	mqttAddr := cfg.MQTTAddrPort()
	if !mqttAddr.IsValid() {
		t.Error("MQTTAddrPort() returned invalid address")
	}
	if mqttAddr.Port() != 1883 {
		t.Errorf("MQTTAddrPort().Port() = %d, want %d", mqttAddr.Port(), 1883)
	}
}
