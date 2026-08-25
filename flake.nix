{
  description = "Zigbee2MQTT HomeKit Bridge - Control Zigbee devices via HomeKit";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem
      (system:
        let
          pkgs = import nixpkgs {
            inherit system;
            overlays = [
              # Several Go tools in nixpkgs are still wired to the default
              # (older) Go. They embed that toolchain, so with `go 1.27.0` in
              # go.mod they try to fetch a newer toolchain at run time -- which
              # fails in sandboxes and offline shells. Rebuild them against
              # go_latest so the whole shell speaks one Go version.
              (_final: prev: {
                gotools = prev.gotools.override { buildGoModule = prev.buildGoLatestModule; };
                gofumpt = prev.gofumpt.override { buildGoModule = prev.buildGoLatestModule; };
                go-tools = prev.go-tools.override { buildGoModule = prev.buildGoLatestModule; };
                delve = prev.delve.override { buildGoModule = prev.buildGoLatestModule; };
              })
            ];
          };
          lib = pkgs.lib;

          go = pkgs.go_latest;

          buildGoModule = pkgs.buildGoLatestModule;

        in
        {
          # Development shell
          devShells.default = pkgs.mkShell {
            buildInputs = with pkgs; [
              go
              golangci-lint
              gofumpt
              gopls
              gotools
              go-tools
              delve

              # Nix tooling
              nixpkgs-fmt

              # Pre-commit hooks
              prek
              prettier

              # Useful utilities
              git
            ];

            # Everything in this shell is already Go 1.27; never let the
            # toolchain switcher reach for the network.
            GOTOOLCHAIN = "local";
          };

          # Package definition
          packages.default = buildGoModule {
            pname = "z2m-homekit";
            version = self.rev or "dev";

            src = ./.;
            subPackages = [ "cmd/z2m-homekit" ];
            vendorHash = "sha256-SseOi8n5DCskSN6Ks2GMaz0yMYcz1WGIGPiqeCcJ9P4=";

            ldflags = [
              "-s"
              "-w"
              "-X github.com/kradalby/z2m-homekit.version=${self.rev or "dev"}"
            ];

            meta = with pkgs.lib; {
              description = "HomeKit bridge for Zigbee2MQTT devices";
              homepage = "https://github.com/kradalby/z2m-homekit";
              license = licenses.mit;
              maintainers = [ ];
            };
          };

          # Alias for the package
          packages.z2m-homekit = self.packages.${system}.default;

          apps = {
            test = {
              type = "app";
              program = toString (pkgs.writeShellScript "test" ''
                set -euo pipefail
                echo "Running go test ./..."
                ${go}/bin/go test -v ./...
              '');
            };

            lint = {
              type = "app";
              program = toString (pkgs.writeShellScript "lint" ''
                set -euo pipefail
                echo "Running golangci-lint..."
                ${pkgs.golangci-lint}/bin/golangci-lint run ./...
              '');
            };

            test-race = {
              type = "app";
              program = toString (pkgs.writeShellScript "test-race" ''
                set -euo pipefail
                echo "Running go test -race ./..."
                ${go}/bin/go test -race ./...
              '');
            };

            coverage = {
              type = "app";
              program = toString (pkgs.writeShellScript "coverage" ''
                set -euo pipefail
                echo "Generating coverage report..."
                ${go}/bin/go test -coverprofile=coverage.out ./...
                ${go}/bin/go tool cover -html=coverage.out -o coverage.html
                echo "Coverage report written to coverage.html"
              '');
            };
          };

          checks =
            {
              package = self.packages.${system}.default;
            }
            // pkgs.lib.optionalAttrs pkgs.stdenv.isLinux {
              module-test = import ./nix/test.nix { inherit pkgs system self; };
            };
        }
      ) // {
      nixosModules.default = import ./nix/module.nix;
      overlays.default = final: prev: {
        z2m-homekit = self.packages.${final.system}.default;
      };
    };
}
