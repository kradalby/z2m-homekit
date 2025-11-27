{ pkgs, system, self }:

pkgs.testers.nixosTest {
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
              "features": {"temperature": true, "humidity": true}
            },
            {
              "id": "test-light",
              "name": "Test Light",
              "topic": "test-light",
              "type": "lightbulb",
              "features": {"brightness": true}
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

    # Wait for web server to be ready
    machine.wait_for_open_port(8081)

    # Check web interface responds (verify page title is present)
    machine.succeed("curl -sf http://localhost:8081/ > /tmp/page.html")
    machine.succeed("grep -q 'z2m-homekit' /tmp/page.html")

    # Check health endpoint
    machine.succeed("curl -sf http://localhost:8081/health | grep -q 'ok'")

    # Check MQTT broker is listening
    machine.wait_for_open_port(1883)

    # Check HAP server is listening
    machine.wait_for_open_port(51826)
  '';
}
