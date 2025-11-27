# Kradalby Software Implementation Guide

This guide captures common practices across all projects—services like `tasmota-homekit` / `nefit-homekit`, libraries like `kra`, `nefit-go`, `tasmota-go`, and anything new we build. It is intentionally generic so future software (web apps, CLIs, daemons, libraries) can follow the same expectations.

---

## 1. Design Philosophy

- **Event-driven mindset**
  - Prefer typed event buses (`tailscale.com/util/eventbus`) for internal communication.
  - Components publish/subscribe instead of reaching into each other.

- **Simplicity first**
  - Small packages, no unnecessary abstractions.
  - If multiple repos need the same helper, promote it to `kra` (or another shared library) instead of copying code.

- **Twelve-Factor discipline**
  - Store config in env vars, log to stdout/stderr via `slog`, keep admin tasks in code.
  - Make services stateless where possible; persist state in `dataDir`-style directories managed by NixOS modules.

- **Observability baked in**
  - Start with `slog`, Prometheus metrics, and Tailscale `tsweb.DebugHandler` endpoints from day one.

- **Nix-first**
  - Services include NixOS modules + flake apps (`test`, `lint`, `coverage`, `package`).
  - Libraries ship `flake.nix` with build/test/lint tooling but no systemd module.

---

## 2. Development Workflow

1. **Use `nix develop`** for every repo. Toolchains (Go, golangci-lint, gofumpt, nixpkgs-fmt, etc.) are pinned there.
2. **Run flake apps** instead of custom scripts:
   - `nix run .#test` → `go test ./...`
   - `nix run .#test-race`
   - `nix run .#lint` → `golangci-lint run`
   - `nix run .#coverage`
   - `nix flake check`
3. **Prek** orchestrates formatting/lint hooks. `prek run --all-files` is mandatory before commits.
4. **CI mirrors local commands** (macOS + Linux).

---

## 3. Foundational Libraries & Tools

| Component | Description & When to Use |
| --- | --- |
| `kra/web` | Local + Tailscale HTTP server (tsnet) with muxes, `/metrics`, `/debug`, graceful shutdown. Use it for any web UI or admin surface. |
| `kra/html` | elem-go snippets for buttons/lists/etc. Use when building dashboards or HTML snippets. |
| `tailscale.com/util/eventbus` | Typed event bus for state/command propagation in any service, not just HomeKit. Keeps components decoupled. |
| `github.com/chasefleming/elem-go` | Declarative HTML builder. Pair with HTMX + SSE for interactive frontends. |
| `htmx` | Lightweight progressive enhancement for forms/buttons (`hx-post`, `hx-target`). |
| `Server-Sent Events` | Use for pushing state changes to browsers (`/events`). |
| `log/slog` | Default logger. Pass it via constructors; no `fmt.Println`. |
| `github.com/Netflix/go-env` | Typed env loader with defaults + validation. Every service has a `config` package wrapping it. |
| `tailscale.com/*` | tsnet, tsweb, local client APIs, `netaddr`, `derp`, etc. Use them for networking, TLS, and debugging rather than writing custom code. |
| `github.com/kradalby/homekit-qr` | ASCII QR generator; relevant for any project exposing HomeKit pairing. |
| `github.com/kradalby/nefit-go` / `tasmota-go` | Device clients; follow their patterns when writing new libraries (contexts, typed enums, backlog helpers). |
| `github.com/mochi-mqtt/server/v2` | Embedded MQTT broker when a service needs to ingest/publish MQTT without external infrastructure. |

> **Guideline:** When a project needs an abstraction that doesn’t exist yet, decide whether it belongs in `kra` (shared helper) or inside the project. Favor promoting generic pieces to `kra` so future work can reuse them.

---

## 4. Application Architecture Pattern

> **Package layout:** keep Go packages at the repo root. Avoid `pkg/`, `internal/`, or deep nesting unless a directory truly represents a specific feature (e.g., `web/`, `config/`). Related packages can live under a single directory, but do not introduce generic container directories.

### 4.1 Configuration (`config/`)
- Load env vars via go-env (`<SERVICE>_*`).
- Validate pins/ports/paths and expose parsed helpers (`AddrPort`, durations, booleans).
- Provide `SetListenerAddrsForTesting` so unit tests can override sockets.

### 4.2 Event Bus
- Instantiate a bus client per component (web, device client, metrics, etc.).
- Define typed events shared across packages.
- Use channels + goroutines to process events; respect context cancellations.

### 4.3 Core Components
- **Device clients / business logic** – long-running goroutines that connect to external systems, publish state, and handle commands. Add retry/backoff logic and structured logging.
- **Web UIs** – built with `kra/web`, elem-go, HTMX, SSE. Always expose `/metrics`, `/health`, `/debug/*`, `/qrcode` (if needed), and any domain endpoints (`/toggle`, `/api/...`).
- **CLI / background workers** – follow the same event-driven pattern; share config + logging packages.

### 4.4 Observability
- `slog` for logs.
- Prometheus metrics via `kra/web`.
- `tsweb.DebugHandler` for Tailscale-only debug endpoints.
- SSE feeds for browser dashboards.

### 4.5 NixOS Modules
- Every service ships a NixOS module exposing:
  - `services.<name>.enable`, `.package`, `.environmentFile`, `.environment`.
  - Ports/listeners (`hap`, `web`, `mqtt`, etc.).
  - Logging options, data directories, firewall toggles.
  - Optional Tailscale auth (pass via `LoadCredential`).
- Modules create service users, directories, systemd hardening, and ensure config lives in `/etc/<service>/env` (or agenix-managed files).

### 4.6 Libraries
- Provide `flake.nix` with devShell (Go toolchain, golangci-lint, gofumpt) and apps for `test`, `lint`, `coverage`.
- No NixOS module is needed unless the library provides a daemon.

---

## 5. Configuration & Twelve-Factor Practices

- **Environment variables** are the source of truth. Store them in `/etc/<service>/env` (or agenix, SOPS, etc.).
- **Secret separation** – secrets go into env files; non-secret defaults live in `config` defaults.
- **State directories** – use `dataDir` (from the module) for persistent artifacts (HomeKit storage, Tailscale state, caches).
- **No adhoc config files** – if you need structured data (like `plugs.hujson`), mount it read-only via `environment.etc` so it remains version controlled.

---

## 6. Web UI & UX Expectations

- Use elem-go for markup.
- Inline CSS via `<style>` with system fonts, accessible colors, responsive grids.
- HTMX for dynamic forms/buttons; SSE for live updates (mirrors eventbus payloads).
- QR/pairing banners use `<details>` + `<summary>` with instructions and links to `/qrcode`.
- Provide event logs, connection indicators, timestamps, and clear button states.

---

## 7. HomeKit & Device-Specific Patterns

- **HomeKit (brutella/hap)**: accessories subscribe to the eventbus and publish commands on remote updates. Store metadata (min/max/step) with characteristics.
- **Nefit**: use `nefit-go` high-level helpers where possible; manual setpoints require `{"value": float}` payloads. Log each command attempt.
- **Tasmota**: `tasmota-go` handles HTTP commands with contexts/timeouts. For atomic updates, use backlog or typed config structs.
- **MQTT**: if embedding a broker (`mochi`), keep it in a dedicated package and bridge through the eventbus.

> Even if a new project isn’t HomeKit-related, follow the same pattern: device-specific code stays isolated, communicates via events, and surfaces control through web/CLI components.

---

## 8. Testing & CI

- `go test ./...` and `go test -race ./...` are mandatory (run via flake apps).
- Aim for >90% coverage on core packages; use `nix run .#coverage` to report.
- `golangci-lint run` (inside `nix develop`) enforces formatting, vetting, and custom linters.
- `nix flake check` validates packages, overlays, modules, and VM tests.
- CI (GitHub Actions) runs the same commands on macOS + Linux.

---

## 9. Documentation Rules

- README: concise usage/setup instructions.
- `<SERVICE>_IMPLEMENTATION.md` or `<SERVICE>_PLAN.md` – working documents **during development only** (not intended for git history once the implementation is complete). Move permanent knowledge into README, module docs, or this guide.
- `AGENTS.md` – workflow instructions for the repo; no author credits.
- `HOMEKIT_SERVICES_PLAN.md` – central plan for multi-repo efforts; keep it updated instead of spawning new plan files.

---

## 10. Reuse Reference

- `kra/web/web.go` – reference for HTTP + Tailscale serving.
- `nefit-homekit/web/server.go`, `tasmota-homekit/web.go` – complete examples of elem-go + HTMX + SSE dashboards.
- `nefit-homekit/nefit/client.go`, `tasmota-homekit/mqtt.go` – long-running clients wired into eventbus with retry/backoff patterns.
- `tasmota-go`, `nefit-go` – high-quality library patterns (contexts, typed enums, CLI tools, docs).

---

## 11. Quick Checklist for New Projects

1. Create `config/` using go-env with `<PROJECT>_*` env vars.
2. Set up `pkg/events` (or similar) and wire `tailscale.com/util/eventbus` early.
3. Build logging (`slog`), metrics, and `tsweb.DebugHandler` endpoints on day one.
4. Add a `kra/web` server if the project has any HTTP/UI surface.
5. Decide whether new helpers belong in `kra`; upstream them instead of duplicating.
6. Add `flake.nix` with devShell + apps; if it’s a service, add a NixOS module.
7. Document usage in README, record temporary implementation notes in `<SERVICE>_PLAN.md` (don’t keep outdated plan files in git once the phase is complete).
8. Run `nix run .#test` / `.#lint` / `nix flake check` before every push; ensure CI stays green.

Following these guidelines keeps repos consistent, debuggable, and easy to extend—regardless of whether the next project is a HomeKit bridge, a CLI utility, or a backend service.

