# NetworkMonitor — v1 design & plan

## Context
New greenfield project in an empty `/home/gtrig/Projects/NetworkMonitor` (not yet a git repo). Goal: a simple self-hosted monitor for a **home lab** that periodically scans several networks (all reachable from one host), shows them as a **topology map over HTTP**, and **notifies on changes** via configurable channels (Telegram, Discord, email).

Decisions from the user:
- One host scans everything it can route to (no remote agents).
- Scan depth: **host discovery only** (IP, MAC, vendor, hostname, up/down) — no port scanning.
- Map: **real topology graph**.
- Network has **two MikroTik routers**. Discovery **sources are configurable per network**: **active** (nmap), **passive SNMP**, **router reporting** (MikroTik, polled via RouterOS API). Several sources may feed one network; results are merged.
- Stack: **Go**.
- Single user, light auth.

## Approach (my calls, correctable)
- **Source plugin model.** `Source` interface: `Collect(ctx, network) (Observations, error)` where Observations = hosts (IP, MAC, hostname, vendor, interface/port, seen-via) **and** links (node↔node edges). Each network's config lists sources; the scheduler runs them on their own intervals and a merger reconciles by MAC (fallback IP), tagging each fact with its source.
- **Active — nmap:** run `nmap -sn -oX -` (host discovery, `-PR` ARP on local nets) and parse XML (`encoding/xml`). Optional `--traceroute` for hop links. Requires nmap on the host (present) / in the Docker image; root or `CAP_NET_RAW` for ARP.
- **Passive — SNMP** (`github.com/gosnmp/gosnmp`, v2c/v3): walk `ipNetToMediaTable` (ARP), `dot1dTpFdbTable`/`dot1qTpFdbTable` (MAC→bridge port), `ifTable` names, and `LLDP-MIB` remote table where supported → hosts + device↔port links. Generic, works for MikroTik and other managed gear.
- **Router reporting — MikroTik RouterOS API** (`github.com/go-routeros/routeros`, port 8728 / TLS 8729; read-only API user): `/ip/arp`, `/ip/dhcp-server/lease` (hostnames!), `/interface/bridge/host` (MAC→port), `/ip/neighbor` (MNDP/LLDP/CDP → router↔router links), `/system/identity`. This gives the best topology: router1 ↔ router2 ↔ bridge ports ↔ hosts.
- **Topology:** graph builder merges links from all sources; hosts without a known port attach to their subnet's gateway router. Vendor from an embedded OUI table; reverse DNS as a fallback for hostnames.
- **Storage:** SQLite via `modernc.org/sqlite` (pure Go, no cgo) → single static binary.
- **Web:** `net/http` + `html/template`, static assets embedded with `embed`; map rendered with **Cytoscape.js** (vendored, no CDN). JSON API backs the UI.
- **Config:** one YAML file (networks, intervals, notifier channels, rules); secrets can come from env vars.
- **Runs as:** binary (nmap needs root/`CAP_NET_RAW` for ARP pings) or Docker image with nmap installed, `network_mode: host` + `cap_add: NET_RAW`.
- **Config sketch:**
  ```yaml
  routers:
    - name: core
      type: mikrotik
      address: 192.168.88.1:8729
      tls: true
      username: netmon-ro
      password: ${CORE_PASS}
  snmp_targets:
    - name: core-snmp
      address: 192.168.88.1
      version: v2c
      community: ${SNMP_COMMUNITY}
  networks:
    - name: lan
      cidr: 192.168.88.0/24
      sources:
        - {type: router, ref: core, interval: 1m}
        - {type: snmp, ref: core-snmp, interval: 5m}
        - {type: nmap, interval: 15m, args: ["-sn"]}
  ```

## Layout
```
cmd/netmon/main.go            # flags, config load, wire everything, graceful shutdown
internal/config/              # YAML schema + validation + env expansion
internal/source/              # Source interface + Observations; merge.go
internal/source/nmap/         # exec + XML parse (tests on fixture XML)
internal/source/snmp/         # gosnmp walks (tests with recorded walk fixtures)
internal/source/mikrotik/     # RouterOS API client (tests with fake API replies)
internal/enrich/              # OUI vendor table, reverse DNS
internal/topology/            # graph builder from merged observations
internal/store/               # SQLite schema/migrations, devices, sightings, events
internal/diff/                # compare scan result vs known state → events
internal/notify/              # Notifier interface; telegram.go, discord.go, email.go; router/rules
internal/scheduler/           # per-network ticker, concurrency limit, manual "scan now"
internal/web/                 # handlers, API, templates, embedded static (cytoscape)
docs/superpowers/specs/2026-11-22-network-monitor-design.md
config.example.yaml, Dockerfile, docker-compose.yml, README.md
```

## Core behaviour
- **Networks** (config): `name`, `cidr`, list of `sources` (router/snmp/nmap, each with its own `interval`), optional exclude list.
- **Device identity:** MAC when known, else IP. Stored: first/last seen, current IP, hostname, vendor, user label/“known” flag (set from UI).
- **Events** (diff engine): `device_new`, `device_offline` (after N consecutive missed scans, default 3, to avoid flapping), `device_back_online`, `ip_changed`, `hostname_changed`, `mac_changed_for_ip` (possible spoof). Every event goes to the DB; the UI shows a timeline.
- **Notifications:** channels defined once (`telegram: bot_token, chat_id`; `discord: webhook_url`; `email: smtp host/port/user/pass/from/to, STARTTLS`). Rules map event types (+ optional network filter) → channels; e.g. only unknown new devices by default. Batch events per scan into one message; retry with backoff; "send test" button in UI.
- **Web UI:** Dashboard (networks, up counts, recent events) · Map (Cytoscape graph: routers (with neighbor links) → bridge ports → hosts; colored by online/new/unknown; click node → details) · Devices table (filter, label/mark known) · Events timeline · "Scan now". Optional HTTP basic auth via config (off by default, bind to localhost unless set).

## Deployment (Docker Compose — primary way to run)
- **Multi-stage `Dockerfile`:** `golang:1.26-alpine` builds a static binary (`CGO_ENABLED=0`); runtime `alpine` with `nmap`, `ca-certificates`, `tzdata`; runs as a non-root user with `cap_net_raw`/`cap_net_admin` granted to nmap via `setcap`; `HEALTHCHECK` on `/healthz`.
- **`docker-compose.yml`:**
  ```yaml
  services:
    netmon:
      build: .
      image: netmon:latest
      container_name: netmon
      restart: unless-stopped
      network_mode: host          # needed for nmap ARP discovery on the LAN
      cap_add: [NET_RAW, NET_ADMIN]
      env_file: .env              # router/SNMP passwords, bot tokens, SMTP creds
      environment:
        NETMON_CONFIG: /config/config.yaml
        NETMON_LISTEN: 0.0.0.0:8080
      volumes:
        - ./config:/config:ro     # config.yaml
        - netmon-data:/data       # SQLite DB
  volumes:
    netmon-data:
  ```
- Ship `config/config.example.yaml` and `.env.example`; `.env` and `config/config.yaml` in `.gitignore`. Config can reference secrets as `${VAR}`.
- README quickstart: `cp .env.example .env && cp config/config.example.yaml config/config.yaml && docker compose up -d` → open `http://<host>:8080`.
- Graceful shutdown on SIGTERM (finish in-flight scans, close DB) so `docker compose down`/restarts are clean.

## Implementation order (TDD where logic is pure)
1. `git init`, Go module, config package + example config, write the design doc to `docs/superpowers/specs/`.
2. Store (schema + migrations) and diff engine — unit tests with table cases.
3. Source interface, merger, fake source; then MikroTik, nmap, SNMP sources (fixture-driven tests); OUI/DNS enrichment.
4. Scheduler wiring sources → merge → store → diff → events.
5. Notifiers + rules router; tests against `httptest` servers and a fake SMTP.
6. Topology graph builder (tests with fake observations incl. router↔router neighbor links).
7. Web: API + templates + Cytoscape map.
8. Dockerfile, docker-compose.yml, `.env.example`, README quickstart.

## Non-goals (v1)
Port/service scanning, OS fingerprinting, remote agents, multi-user/RBAC, router push/webhooks, non-MikroTik router APIs (SNMP covers other gear), IPv6 sweep.

## Verification
- `go test ./...` (diff engine, notifier payloads, rules, graph builder, config validation).
- `go vet` + build static binary (`CGO_ENABLED=0 go build ./cmd/netmon`).
- `docker compose build && docker compose up -d`; check `docker compose ps` shows healthy, `curl localhost:8080/healthz`, data survives `docker compose restart`.
- Run (via compose) against the real LAN with each source enabled one at a time (user creates a read-only RouterOS API user and SNMP community): confirm devices appear with the right source tags, both MikroTiks and their neighbor link show on the map in the built-in browser, and "send test" reaches a configured channel (user supplies their own tokens).
- Simulate change: disconnect/connect a device (or use the fake scanner in a dev mode) → event recorded and notification sent.
