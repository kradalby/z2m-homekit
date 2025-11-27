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
          pkgs = import nixpkgs { inherit system; };
          lib = pkgs.lib;

          go = pkgs.go_1_25;

          buildGoModule = pkgs.buildGoModule.override { go = pkgs.go_1_25; };

        in
        {
          # Development shell
          devShells.default = pkgs.mkShell {
            buildInputs = with pkgs; [
              go
              golangci-lint
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
              gnumake
            ];
          };

          # Package definition
          packages.default = buildGoModule {
            pname = "z2m-homekit";
            version = self.rev or "dev";

            src = ./.;
            subPackages = [ "cmd/z2m-homekit" ];
            vendorHash = "sha256-sooS4+fi96lvKq1LtCZ2SPUWzh1RKvcTIuMOh1caT/A=";

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
