{
  description = "Zigbee2MQTT HomeKit Bridge - Control Zigbee devices via HomeKit";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    flake-checks.url = "github:kradalby/flake-checks";
    flake-checks.inputs.nixpkgs.follows = "nixpkgs";
    flake-checks.inputs.flake-utils.follows = "flake-utils";
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
      flake-checks,
      ...
    }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs {
          inherit system;
          overlays = [
            # Several Go tools in nixpkgs are still wired to the default
            # (older) Go. They embed that toolchain, so with `go 1.27.0` in
            # go.mod they try to fetch a newer one at run time -- which fails
            # in the network-less formatting sandbox and in offline shells.
            # Rebuild them against go_latest so everything speaks one Go.
            #
            # gotools needs both: nixpkgs wraps goimports with a `go` on PATH
            # taken from a separate `go` argument, so overriding only
            # buildGoModule leaves the wrapper pointing at the old toolchain
            # and goimports tries to download go1.27.0 mid-format.
            (_final: prev: {
              gotools = prev.gotools.override {
                buildGoModule = prev.buildGoLatestModule;
                go = prev.go_latest;
              };
              gofumpt = prev.gofumpt.override { buildGoModule = prev.buildGoLatestModule; };
              go-tools = prev.go-tools.override { buildGoModule = prev.buildGoLatestModule; };
              delve = prev.delve.override { buildGoModule = prev.buildGoLatestModule; };
            })
          ];
        };

        fc = flake-checks.lib;
        version = self.shortRev or self.dirtyShortRev or "dev";

        common = {
          inherit pkgs version;
          root = ./.;
          pname = "z2m-homekit";
          vendorHash = "sha256-ehRUYJ0R2dK7+t+VUZb5cyp776chorx6i61aTiNsdBM=";
          goPkg = pkgs.go_latest;
          subPackages = [ "cmd/z2m-homekit" ];
          embedDirs = [ ./assets ];
          ldflags = [
            "-s"
            "-w"
            "-X github.com/kradalby/z2m-homekit.version=${version}"
          ];
        };

        z2m-homekit = (fc.goBuild common).overrideAttrs (_: {
          meta = {
            description = "HomeKit bridge for Zigbee2MQTT devices";
            homepage = "https://github.com/kradalby/z2m-homekit";
            license = pkgs.lib.licenses.mit;
            mainProgram = "z2m-homekit";
          };
        });
      in
      {
        packages.default = z2m-homekit;
        packages.z2m-homekit = z2m-homekit;

        formatter = fc.formatter common;

        checks = {
          build = fc.goBuild common;
          gotest = fc.goTest common;
          gotest-race = fc.goTest (
            common
            // {
              goRace = true;
              name = "z2m-homekit-gotest-race";
            }
          );
          golangci-lint = fc.goLint common;
          formatting = fc.goFormat common;
        }
        // pkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
          module-test = import ./nix/test.nix { inherit pkgs system self; };
        };

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go_latest
            golangci-lint
            gofumpt
            gopls
            gotools
            go-tools
            delve

            # Nix tooling
            nixfmt

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

        apps.default = flake-utils.lib.mkApp {
          drv = z2m-homekit;
        };
      }
    )
    // {
      nixosModules.default = import ./nix/module.nix;
      overlays.default = final: _prev: {
        z2m-homekit = self.packages.${final.system}.default;
      };
    };
}
