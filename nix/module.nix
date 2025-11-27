{ config, lib, pkgs, ... }:

with lib;

let
  cfg = config.services.z2m-homekit;
  hapDir = "${cfg.dataDir}/hap";
  tailscaleDir = "${cfg.dataDir}/tailscale";
in
{
  imports = [
    (mkRenamedOptionModule
      [ "services" "z2m-homekit" "hap" "storagePath" ]
      [ "services" "z2m-homekit" "dataDir" ])
  ];

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
  };

  config = mkIf cfg.enable (mkMerge [
    {
      users.users.${cfg.user} = {
        isSystemUser = true;
        group = cfg.group;
        description = "z2m-homekit service user";
        home = cfg.dataDir;
        createHome = true;
      };

      users.groups.${cfg.group} = { };

      systemd.tmpfiles.rules = [
        "d ${cfg.dataDir} 0750 ${cfg.user} ${cfg.group} - -"
        "d ${hapDir} 0750 ${cfg.user} ${cfg.group} - -"
        "d ${tailscaleDir} 0750 ${cfg.user} ${cfg.group} - -"
      ];
    }

    (mkIf cfg.openFirewall {
      networking.firewall = {
        allowedTCPPorts = [
          cfg.ports.hap
          cfg.ports.web
          cfg.ports.mqtt
        ];
        allowedUDPPorts = [
          5353
        ];
      };
    })

    {
      systemd.services.z2m-homekit =
        let
          envVars = {
            Z2M_HOMEKIT_HAP_ADDR = "${cfg.bindAddresses.hap}:${toString cfg.ports.hap}";
            Z2M_HOMEKIT_WEB_ADDR = "${cfg.bindAddresses.web}:${toString cfg.ports.web}";
            Z2M_HOMEKIT_MQTT_ADDR = "${cfg.bindAddresses.mqtt}:${toString cfg.ports.mqtt}";
            Z2M_HOMEKIT_HAP_PORT = toString cfg.ports.hap;
            Z2M_HOMEKIT_WEB_PORT = toString cfg.ports.web;
            Z2M_HOMEKIT_MQTT_PORT = toString cfg.ports.mqtt;
            Z2M_HOMEKIT_HAP_PIN = cfg.hap.pin;
            Z2M_HOMEKIT_HAP_STORAGE_PATH = hapDir;
            Z2M_HOMEKIT_DEVICES_CONFIG = toString cfg.devicesConfig;
            Z2M_HOMEKIT_LOG_LEVEL = cfg.log.level;
            Z2M_HOMEKIT_LOG_FORMAT = cfg.log.format;
            Z2M_HOMEKIT_TS_HOSTNAME = cfg.tailscale.hostname;
            Z2M_HOMEKIT_TS_STATE_DIR = tailscaleDir;
          }
          // (optionalAttrs (cfg.bridgeName != null) {
            Z2M_HOMEKIT_BRIDGE_NAME = cfg.bridgeName;
          })
          // cfg.environment;

          tailscaleExport =
            optionalString (cfg.tailscale.authKeyFile != null) ''
              export Z2M_HOMEKIT_TS_AUTHKEY="$(cat "$CREDENTIALS_DIRECTORY/tailscale-authkey")"
            '';

          startScript = pkgs.writeShellScript "z2m-homekit-start" ''
            set -euo pipefail
            ${tailscaleExport}
            exec ${cfg.package}/bin/z2m-homekit
          '';
        in
        {
          description = "Zigbee2MQTT HomeKit Bridge";
          documentation = [ "https://github.com/kradalby/z2m-homekit" ];
          wantedBy = [ "multi-user.target" ];
          wants = [ "network-online.target" ];
          after = [ "network-online.target" ];

          unitConfig = {
            StartLimitIntervalSec = "5min";
            StartLimitBurst = 5;
          };

          restartTriggers =
            [ cfg.package cfg.devicesConfig ]
            ++ optional (cfg.environmentFile != null) cfg.environmentFile;

          environment = envVars;

          serviceConfig = {
            Type = "simple";
            ExecStart = startScript;
            User = cfg.user;
            Group = cfg.group;

            Restart = "on-failure";
            RestartSec = "10s";

            WorkingDirectory = cfg.dataDir;

            StandardOutput = "journal";
            StandardError = "journal";
            SyslogIdentifier = "z2m-homekit";
          }
          // (optionalAttrs (cfg.environmentFile != null) {
            EnvironmentFile = cfg.environmentFile;
          })
          // (optionalAttrs (cfg.tailscale.authKeyFile != null) {
            LoadCredential = "tailscale-authkey:${cfg.tailscale.authKeyFile}";
          });
        };
    }
  ]);
}
