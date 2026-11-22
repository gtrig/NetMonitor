# NetworkMonitor v1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Go home-lab network monitor that discovers hosts through configurable sources (MikroTik RouterOS API, SNMP, nmap), shows a topology map over HTTP, and notifies via Telegram/Discord/email on changes, deployable with Docker Compose.

**Architecture:** Per-network sources run on their own intervals and produce `model.Observations` (hosts + links). A merge step normalises/filters them, the diff engine compares them with stored devices to produce events, the store (SQLite) persists devices/links/events, and a dispatcher routes events to notifiers by rules. A `net/http` server serves a JSON API and embedded HTML pages with a Cytoscape.js map.

**Tech Stack:** Go 1.26, `go.yaml.in/yaml/v3`, `modernc.org/sqlite`, `github.com/go-routeros/routeros/v3`, `github.com/gosnmp/gosnmp`, nmap (external binary), Cytoscape.js 3.30.x (vendored), Docker Compose.

**Spec:** `docs/superpowers/specs/2026-11-22-network-monitor-design.md`

## Global Constraints

- Module path `github.com/gtrig/netmon`; binary `cmd/netmon`; must build with `CGO_ENABLED=0`.
- Host discovery only — never port-scan (nmap always `-sn`; reject user args containing `-p`, `-sS`, `-sT`, `-sU`, `-sV`, `-O`, `-A`).
- Config is one YAML file; `${VAR}` references are expanded from the environment before parsing.
- Default listen `127.0.0.1:8080`; `NETMON_LISTEN` and `NETMON_DATA_DIR` env override config `listen` and `data_dir`. `NETMON_CONFIG` env gives the config path (default `./config/config.yaml`). Data dir default `./data` (`/data` in Docker).
- Device identity: `mac:<aa:bb:cc:dd:ee:ff>` (lowercase, colon) when a MAC is known, otherwise `ip:<addr>`.
- Event types (exact strings): `device_new`, `device_offline`, `device_back_online`, `ip_changed`, `hostname_changed`, `mac_changed_for_ip`, `baseline`.
- Default source interval `5m`; default `offline_after` = 3 × the network's smallest source interval.
- No CDN at runtime: all web assets embedded.
- Docker: `network_mode: host`, `cap_add: [NET_RAW, NET_ADMIN]`, volumes `./config:/config:ro` and `netmon-data:/data`, `env_file: .env`, healthcheck on `/healthz`.
- Every commit message ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. **A source fails (router down, wrong password, nmap missing)** → devices must NOT be reported offline; offline is judged against the network's last *successful* collection, not wall clock. (Task 3 test `TestOfflineIgnoresFailedCollections`.)
2. **First run** → no flood of `device_new`; first successful collection for a network with no known devices emits one `baseline` event. (Task 3 `TestFirstCollectionEmitsBaseline`.)
3. **Same device seen with MAC by the router and without MAC by nmap on a routed subnet** → one device, not two. (Task 4 `TestMergeAttachesIPOnlyHostToMACDevice`, Task 3 `TestApplyIPOnlyMatchesKnownMACDevice`.)
4. **Router ARP table contains other subnets / excluded IPs / incomplete entries** → filtered by the network's CIDR and exclude list; incomplete/failed ARP entries ignored. (Task 4 `TestMergeFiltersOutsideCIDRAndExcluded`, Task 5 `TestParseARPSkipsIncomplete`.)
5. **Notifier outage or huge batch** → scans never block on notifications; messages truncated to channel limits (Telegram 4096, Discord 2000 chars) with "…and N more". (Task 8 `TestFormatTruncates`, `TestDispatchDoesNotBlock`.)

---

## File Structure

```
cmd/netmon/main.go                 wiring, signals, graceful shutdown
internal/config/config.go          types, Load, defaults, validation
internal/config/duration.go        Duration YAML type
internal/model/model.go            Host, Link, Observations, Device, Event (shared types, no deps)
internal/store/store.go            Open, migrations
internal/store/devices.go          device CRUD
internal/store/events.go           event insert/query
internal/store/links.go            links per network+source
internal/diff/diff.go              Apply, Offline
internal/source/source.go          Source interface, Merge
internal/source/mikrotik/          mikrotik.go (+ parse.go)
internal/source/nmap/              nmap.go (+ parse.go, testdata/*.xml)
internal/source/snmp/              snmp.go (+ parse.go)
internal/enrich/oui.go             OUI vendor lookup (nmap-mac-prefixes format)
internal/enrich/dns.go             reverse DNS with cache
internal/notify/notify.go          Notifier, Message, Format
internal/notify/telegram.go|discord.go|email.go
internal/notify/dispatch.go        rules → channels, async queue, retry
internal/topology/topology.go      Build graph (Cytoscape elements)
internal/scheduler/scheduler.go    per-source tickers, pipeline, ScanNow
internal/web/server.go             routes, basic auth, handlers
internal/web/static/               cytoscape.min.js, app.js, map.js, style.css
internal/web/templates/            layout, dashboard, map, devices, events (.html)
config/config.example.yaml, .env.example, .gitignore, Dockerfile, docker-compose.yml, README.md
```

---

### Task 1: Config

**Files:**
- Create: `internal/config/config.go`, `internal/config/duration.go`, `config/config.example.yaml`, `.gitignore`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces:
  ```go
  type Duration struct{ time.Duration }            // YAML "5m"
  type Config struct {
      Listen, DataDir, OUIPath string               // yaml: listen, data_dir, oui_path (default /usr/share/nmap/nmap-mac-prefixes)
      Auth     *BasicAuth                           // yaml: auth {username,password}
      Routers  []Router; SNMPTargets []SNMPTarget   // yaml: routers, snmp_targets
      Networks []Network; Channels []Channel; Rules []Rule
  }
  type Router struct{ Name, Type, Address, Username, Password string; TLS, InsecureSkipVerify bool }  // Type: "mikrotik"
  type SNMPTarget struct{ Name, Address, Version, Community, Username, AuthProto, AuthPass, PrivProto, PrivPass string; Port int } // Version "v2c"|"v3"; Port default 161
  type Network struct{ Name string; CIDR netip.Prefix; Sources []SourceRef; Exclude []netip.Addr; OfflineAfter Duration }
  type SourceRef struct{ Type, Ref string; Interval Duration; Args []string }  // Type "router"|"snmp"|"nmap"
  type Channel struct{ Name, Type, BotToken, ChatID, WebhookURL, SMTPHost, Username, Password, From string; SMTPPort int; To []string; StartTLS bool } // Type "telegram"|"discord"|"email"
  type Rule struct{ Events, Networks, Channels []string; OnlyUnknown bool }
  func Load(path string) (*Config, error)          // read, os.ExpandEnv, yaml decode (KnownFields), applyDefaults, Validate
  func Parse(data []byte) (*Config, error)         // same minus file read (used by tests)
  func (c *Config) Validate() error                // joins all problems with errors.Join
  ```

- [ ] **Step 1: Write failing tests**
  - `TestParseExample`: `Load("../../config/config.example.yaml")` with env `CORE_PASS=x` succeeds; `Networks[0].Sources` has 3 entries; router password == `"x"`.
  - `TestDefaults`: minimal config (one network, one nmap source without interval) → `Listen=="127.0.0.1:8080"`, `DataDir=="./data"`, source interval `5m`, `OfflineAfter==15m`.
  - `TestValidateErrors` (table): unknown source type; `ref` to missing router; duplicate network name; rule referencing unknown channel; unknown event type in rule; nmap args containing `-p` → each returns error whose message contains the offending name.
  - `TestEnvOverrides`: `NETMON_LISTEN=0.0.0.0:9000`, `NETMON_DATA_DIR=/data` → `Listen=="0.0.0.0:9000"`, `DataDir=="/data"`.
- [ ] **Step 2:** `go test ./internal/config/` → FAIL (undefined).
- [ ] **Step 3: Implement.** Write `config.example.yaml` using the spec's config sketch plus channels (`telegram`, `discord`, `email`) and one rule `{events: [device_new], only_unknown: true, channels: [telegram]}`; secrets as `${VAR}`. `.gitignore`: `/data/`, `.env`, `config/config.yaml`, `/netmon`.
- [ ] **Step 4:** `go test ./internal/config/` → PASS.
- [ ] **Step 5:** Commit `feat(config): YAML config with env expansion and validation`.

### Task 2: Model and store

**Files:**
- Create: `internal/model/model.go`, `internal/store/{store,devices,events,links}.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Produces (`model`):
  ```go
  type Host struct{ IP netip.Addr; MAC, Hostname, Vendor, Port, Source string }   // Port = bridge/interface where seen
  type NodeRef struct{ Kind, ID string }   // Kind: "router" (ID = router name/identity) | "device" (ID = device key)
  type Link struct{ A, B NodeRef; Label, Source string }
  type Observations struct{ Network, Source string; At time.Time; Hosts []Host; Links []Link }
  func DeviceKey(h Host) string            // "mac:..." or "ip:..."
  func NormalizeMAC(s string) string       // accepts aa-bb.., AABB.CCDD.EEFF, aa:bb..; "" if invalid
  type Device struct{ Key, Network, MAC, Hostname, Vendor, Label, Port string; IP netip.Addr; Known, Online bool; FirstSeen, LastSeen time.Time; Sources []string }
  type Event struct{ ID int64; Type, Network, DeviceKey, Message string; At time.Time; Old, New string }
  ```
- Produces (`store`):
  ```go
  func Open(path string) (*Store, error)   // WAL, busy_timeout=5000, runs migrations
  func (s *Store) Close() error
  func (s *Store) Devices(ctx, network string) ([]model.Device, error)   // network "" = all
  func (s *Store) UpsertDevices(ctx, []model.Device) error
  func (s *Store) SetDeviceMeta(ctx, key string, label *string, known *bool) error  // ErrNotFound
  func (s *Store) AddEvents(ctx, []model.Event) error
  func (s *Store) Events(ctx, EventFilter) ([]model.Event, error)   // EventFilter{Network string; Limit int; Before int64}; newest first
  func (s *Store) ReplaceLinks(ctx, network, source string, []model.Link) error
  func (s *Store) Links(ctx, network string) ([]model.Link, error)
  func (s *Store) LastSuccess(ctx, network string) (time.Time, error) / SetLastSuccess(ctx, network string, t time.Time) error
  ```

- [ ] **Step 1: Write failing tests** (temp-dir DB):
  - `TestNormalizeMAC` (model): `"AA-BB-CC-00-11-22"`, `"aabb.cc00.1122"` → `"aa:bb:cc:00:11:22"`; `"zz"` → `""`.
  - `TestDeviceRoundTrip`: upsert device with 2 sources, read back equal (times truncated to second).
  - `TestSetDeviceMetaNotFound`: unknown key → `errors.Is(err, store.ErrNotFound)`.
  - `TestEventsNewestFirstAndLimit`: insert 5, `Limit:2` → IDs 5,4.
  - `TestReplaceLinksScopedBySource`: links from source A survive replacing source B's links.
  - `TestReopenKeepsData`: close and reopen same path → devices still there.
- [ ] **Step 2:** run → FAIL. **Step 3:** implement; migrations as ordered SQL strings tracked in `schema_version`. **Step 4:** `go test ./internal/...` → PASS.
- [ ] **Step 5:** Commit `feat(store): SQLite store for devices, events, links`.

### Task 3: Diff engine

**Files:** Create `internal/diff/diff.go`; Test `internal/diff/diff_test.go`

**Interfaces:**
- Consumes: `model.*`.
- Produces:
  ```go
  // Apply merges one network's observations into known devices.
  // firstEver: network has never had a successful collection → emit single "baseline" event instead of per-device device_new.
  func Apply(known []model.Device, obs model.Observations, firstEver bool) (changed []model.Device, events []model.Event)
  // Offline marks online devices whose LastSeen is older than lastSuccess-offlineAfter.
  func Offline(known []model.Device, network string, lastSuccess time.Time, offlineAfter time.Duration) (changed []model.Device, events []model.Event)
  ```
  Matching order per observed host: by MAC key; else IP-only host matches the known device whose current IP equals it (MAC device preferred). `mac_changed_for_ip` when an IP previously held by MAC X is now reported with MAC Y (both MACs present). Empty observed hostname never overwrites a known one. Device `Sources` is the union.

- [ ] **Step 1: Write failing tests** (table-driven where natural):
  - `TestFirstCollectionEmitsBaseline`: no known, 3 hosts, firstEver → 3 changed devices, exactly 1 event `baseline` with Message `"Baseline: 3 devices discovered"`.
  - `TestNewDevice`: known 1, obs adds a second → one `device_new` with DeviceKey of the new host.
  - `TestIPChanged`, `TestHostnameChanged` (Old/New set), `TestEmptyHostnameKeepsOld`.
  - `TestBackOnline`: known Online=false, observed → `device_back_online`, Online=true.
  - `TestMACChangedForIP`: known mac:A at 10.0.0.5; obs mac:B at 10.0.0.5 → `mac_changed_for_ip` (plus `device_new` for B).
  - `TestApplyIPOnlyMatchesKnownMACDevice`: known mac:A @10.0.0.5; obs IP-only 10.0.0.5 → no new device, LastSeen updated on mac:A.
  - `TestOfflineAfterThreshold`: LastSeen = lastSuccess−20m, after 15m → `device_offline`; LastSeen = lastSuccess−10m → nothing.
  - `TestOfflineIgnoresFailedCollections`: lastSuccess unchanged at T, LastSeen=T → nothing even when called "an hour later" (function never reads wall clock).
- [ ] **Step 2–4:** run FAIL → implement → PASS.
- [ ] **Step 5:** Commit `feat(diff): change detection with baseline and offline logic`.

### Task 4: Source interface, merge, enrichment

**Files:** Create `internal/source/source.go`, `internal/enrich/{oui,dns}.go`; Tests `internal/source/source_test.go`, `internal/enrich/oui_test.go`

**Interfaces:**
- Produces:
  ```go
  type Source interface { Name() string; Collect(ctx context.Context, n config.Network) (model.Observations, error) }
  // Merge: normalise MACs, drop hosts outside n.CIDR or in n.Exclude, collapse duplicates (same key → combine fields, first non-empty wins, Port from router source preferred),
  // attach IP-only hosts to a MAC host with the same IP.
  func Merge(n config.Network, obs model.Observations) model.Observations
  type OUI struct{...}; func LoadOUI(path string) (*OUI, error); func (o *OUI) Vendor(mac string) string   // "" if unknown; nil-safe
  type Resolver struct{...}; func NewResolver(ttl time.Duration) *Resolver; func (r *Resolver) Name(ctx, ip netip.Addr) string // trailing dot trimmed, 1s timeout, cached
  func Enrich(obs *model.Observations, oui *OUI, r *Resolver)   // fills empty Vendor / Hostname
  ```
- [ ] **Step 1: Failing tests:** `TestMergeFiltersOutsideCIDRAndExcluded`; `TestMergeAttachesIPOnlyHostToMACDevice`; `TestMergeCollapsesDuplicates`; `TestOUIVendor` using a 3-line fixture `testdata/prefixes` (`"000C42 Routerboard.com"` → `Vendor("00:0c:42:aa:bb:cc")=="Routerboard.com"`); `TestEnrichKeepsExistingVendor`.
- [ ] **Step 2–4:** FAIL → implement (`nmap-mac-prefixes` format: 6 hex chars, space, name; skip `#`) → PASS.
- [ ] **Step 5:** Commit `feat(source): source interface, merge and enrichment`.

### Task 5: MikroTik source

**Files:** Create `internal/source/mikrotik/{mikrotik,parse}.go`; Test `internal/source/mikrotik/mikrotik_test.go`

**Interfaces:**
- Consumes: `config.Router`, `config.Network`, `model.*`.
- Produces:
  ```go
  type Runner interface { Print(ctx context.Context, path string) ([]map[string]string, error) }  // e.g. "/ip/arp/print"
  func New(r config.Router) *Source                         // dials via routeros v3 (DialTLS when r.TLS) lazily, reconnects on error
  func NewWithRunner(name string, run Runner) *Source        // tests
  func (s *Source) Name() string                             // "router:<name>"
  func (s *Source) Collect(ctx, n config.Network) (model.Observations, error)
  func parseARP(rows []map[string]string) []model.Host       // skip rows where status is "incomplete"/"failed" or "invalid"/"disabled"=="true", or no mac-address
  func parseLeases(rows) map[string]string                   // MAC → host-name (used only to fill Hostname, never presence)
  func parseBridgeHosts(rows) map[string]string              // MAC → "bridge/on-interface" for non-"local" entries
  func parseNeighbors(identity string, rows) []model.Link    // router↔router link per row with identity, Label = "interface"
  ```
  Collect reads `/system/identity` (`name`), `/ip/arp`, `/ip/dhcp-server/lease`, `/interface/bridge/host`, `/ip/neighbor`. Hosts get Port from bridge hosts; link `router:<identity>` ↔ `device:<key>` for each host with a Port, Label = port. A failure of the ARP call fails Collect; failures of the others are logged and skipped.
- [ ] **Step 1: Failing tests** with a fake Runner returning canned rows: `TestParseARPSkipsIncomplete`; `TestCollectUsesLeaseHostname`; `TestCollectNeighborLinks` (identity "core", neighbor identity "ap-attic" → Link router:core↔router:ap-attic); `TestCollectARPErrorFails`; `TestCollectLeaseErrorStillReturnsHosts`.
- [ ] **Step 2–4:** FAIL → implement → PASS. `go get github.com/go-routeros/routeros/v3@v3.0.1`.
- [ ] **Step 5:** Commit `feat(source): MikroTik RouterOS API source`.

### Task 6: nmap source

**Files:** Create `internal/source/nmap/{nmap,parse}.go`, `internal/source/nmap/testdata/{lan.xml,traceroute.xml}`; Test `nmap_test.go`

**Interfaces:**
- Produces:
  ```go
  type ExecFunc func(ctx context.Context, args ...string) ([]byte, error)   // default: exec.CommandContext("nmap", ...).Output()
  func New(args []string, exec ExecFunc) *Source          // Name() "nmap"
  func BuildArgs(user []string, cidr netip.Prefix) []string   // always ["-sn","-oX","-", ...user..., cidr]; dedupes -sn
  func ParseXML(data []byte) ([]model.Host, []model.Link, error)
  ```
  ParseXML: host `status state="up"`; `address addrtype="ipv4"`, `addrtype="mac"` (+ `vendor` attr), first `hostnames/hostname@name`; with `<trace>` emit router-hop links between consecutive hop IPs (`router:<ip>`), last hop → device.
- [ ] **Step 1: Failing tests:** `TestParseXMLHosts` (fixture with 2 up + 1 down → 2 hosts, MAC+vendor on one); `TestParseXMLTraceroute`; `TestBuildArgs`; `TestCollectNmapMissing` (ExecFunc returns `exec.ErrNotFound` → error mentions "nmap not found").
- [ ] **Step 2–4:** FAIL → implement → PASS. Write fixtures by hand in nmap's XML shape (`nmaprun/host/status|address|hostnames|trace/hop@ipaddr,ttl`).
- [ ] **Step 5:** Commit `feat(source): nmap host-discovery source`.

### Task 7: SNMP source

**Files:** Create `internal/source/snmp/{snmp,parse}.go`; Test `snmp_test.go`

**Interfaces:**
- Produces:
  ```go
  type Walker interface { BulkWalkAll(oid string) ([]gosnmp.SnmpPDU, error) }
  func New(t config.SNMPTarget) *Source            // Name() "snmp:<name>"; connects per Collect, 5s timeout, 2 retries
  func NewWithWalker(name string, w Walker) *Source
  func parseARP(pdus) []model.Host                 // ipNetToMediaPhysAddress .1.3.6.1.2.1.4.22.1.2.<ifIndex>.<a.b.c.d>
  func parseFDB(pdus, bridgePortToIf map[int]int, ifNames map[int]string) map[string]string  // MAC→ifName; dot1dTpFdbPort .1.3.6.1.2.1.17.4.3.1.2.<mac 6 octets>, dot1dBasePortIfIndex .1.3.6.1.2.1.17.1.4.1.2, ifName .1.3.6.1.2.1.31.1.1.1.1
  func parseLLDP(local string, pdus) []model.Link   // lldpRemSysName .1.0.8802.1.1.2.1.4.1.1.9
  ```
  Local identity = sysName `.1.3.6.1.2.1.1.5.0`. ARP walk failure fails Collect; FDB/LLDP failures are skipped (not all devices support them).
- [ ] **Step 1: Failing tests** using fake Walker with hand-built PDUs: `TestParseARP`, `TestParseFDB`, `TestParseLLDP`, `TestCollectWithoutLLDP`.
- [ ] **Step 2–4:** FAIL → implement → PASS. `go get github.com/gosnmp/gosnmp@latest`.
- [ ] **Step 5:** Commit `feat(source): SNMP source (ARP, FDB, LLDP)`.

### Task 8: Notifiers and dispatcher

**Files:** Create `internal/notify/{notify,telegram,discord,email,dispatch}.go`; Tests `notify_test.go`, `dispatch_test.go`

**Interfaces:**
- Produces:
  ```go
  type Message struct{ Title string; Lines []string }
  type Notifier interface { Name() string; Send(ctx context.Context, m Message) error }
  func Format(m Message, limit int) string          // "Title\n- line\n…"; if over limit: keep lines that fit + "…and N more"
  func NewTelegram(c config.Channel, client *http.Client, baseURL string) Notifier  // POST {base}/bot<token>/sendMessage {chat_id,text}; limit 4096; baseURL default https://api.telegram.org
  func NewDiscord(c config.Channel, client *http.Client) Notifier                   // POST webhook {content}; limit 2000
  func NewEmail(c config.Channel, dial SMTPSendFunc) Notifier                       // net/smtp, STARTTLS when set; Subject = Title
  func FromConfig(chs []config.Channel) (map[string]Notifier, error)
  type Dispatcher struct{...}
  func NewDispatcher(rules []config.Rule, ns map[string]Notifier, devices func(key string) (known bool)) *Dispatcher
  func (d *Dispatcher) Start(ctx context.Context)     // worker goroutine; 3 retries, backoff 2s/4s/8s, 10s per-send timeout
  func (d *Dispatcher) Enqueue(events []model.Event)  // non-blocking; buffered chan 100, drops+logs when full
  func (d *Dispatcher) Test(ctx, channel string) error
  ```
  Routing: an event matches a rule if Type ∈ rule.Events (empty = all), Network ∈ rule.Networks (empty = all), and not (OnlyUnknown && known). One Message per channel per Enqueue call, Title `"netmon: N change(s) on <network>"`.
- [ ] **Step 1: Failing tests:** `TestFormatTruncates` (100 lines, limit 200 → len ≤ 200, ends with "…and N more"); `TestTelegramPayload` & `TestDiscordPayload` (httptest server asserts path/JSON); `TestEmailMessage` (fake SMTPSendFunc captures To/body with `Subject:`); `TestRulesOnlyUnknown`; `TestDispatchDoesNotBlock` (notifier blocks forever; 200 Enqueue calls return within 100ms); `TestDispatchRetries` (fails twice then succeeds → 3 calls, using a tiny backoff override).
- [ ] **Step 2–4:** FAIL → implement → PASS.
- [ ] **Step 5:** Commit `feat(notify): telegram, discord, email notifiers and rule dispatcher`.

### Task 9: Topology graph

**Files:** Create `internal/topology/topology.go`; Test `topology_test.go`

**Interfaces:**
- Produces:
  ```go
  type Element struct{ Group string `json:"group"`; Data map[string]any `json:"data"` }  // Cytoscape format; Group "nodes"|"edges"
  func Build(devices []model.Device, links []model.Link, gateways map[string]string) []Element
  ```
  Nodes: one per router NodeRef seen in links (`kind:"router"`), one per device (`kind:"device"`, `status:"online"|"offline"`, `known`, `label` = Label||Hostname||IP, `ip`, `mac`, `vendor`, `network`). Edges from links (deduped regardless of direction/source). Devices with no edge attach to `gateways[device.Network]` router node (created if missing; gateway = configured router for that network's first router source, else a synthetic `network:<name>` node).
- [ ] **Step 1: Failing tests:** `TestBuildRouterNeighborEdge`; `TestBuildOrphanAttachesToGateway`; `TestBuildDedupesBidirectionalLinks`.
- [ ] **Step 2–4:** FAIL → implement → PASS.
- [ ] **Step 5:** Commit `feat(topology): build cytoscape graph`.

### Task 10: Scheduler and main

**Files:** Create `internal/scheduler/scheduler.go`, `cmd/netmon/main.go`; Test `internal/scheduler/scheduler_test.go`

**Interfaces:**
- Consumes: `source.Source`, `source.Merge`, `enrich.Enrich`, `diff.Apply/Offline`, `store.*`, `notify.Dispatcher`.
- Produces:
  ```go
  type Job struct{ Network config.Network; Source source.Source; Interval time.Duration }
  func New(st *store.Store, d EventSink, oui *enrich.OUI, r *enrich.Resolver, jobs []Job) *Scheduler   // EventSink interface{ Enqueue([]model.Event) }
  func (s *Scheduler) Run(ctx context.Context)        // each job: run immediately, then every Interval; max 4 concurrent collections; per-network mutex
  func (s *Scheduler) ScanNow(network string) error   // triggers all jobs of network; ErrUnknownNetwork
  func (s *Scheduler) RunOnce(ctx, j Job) error       // pipeline: Collect → Merge → Enrich → Apply → Upsert → ReplaceLinks → SetLastSuccess → Offline → AddEvents → Enqueue
  func (s *Scheduler) Status() []JobStatus            // {Network, Source string; LastRun time.Time; LastErr string}
  ```
  `main`: load config, open store at `<data_dir>/netmon.db`, build sources from config (router→mikrotik.New, snmp→snmp.New, nmap→nmap.New), dispatcher, scheduler, web server; SIGINT/SIGTERM → cancel ctx, `http.Server.Shutdown` with 10s timeout, wait for in-flight collections, close store. Flag `-config` overrides `NETMON_CONFIG`.
- [ ] **Step 1: Failing tests** with a fake Source and a fake sink: `TestRunOncePersistsAndEnqueues` (first run → baseline event enqueued, devices stored); `TestRunOnceSourceErrorKeepsLastSuccess`; `TestScanNowUnknownNetwork`.
- [ ] **Step 2–4:** FAIL → implement → PASS; `go build ./cmd/netmon` succeeds.
- [ ] **Step 5:** Commit `feat: scheduler pipeline and main wiring`.

### Task 11: Web UI and API

**Files:** Create `internal/web/server.go`, `internal/web/templates/*.html`, `internal/web/static/{cytoscape.min.js,map.js,app.js,style.css}`; Test `internal/web/server_test.go`

**Interfaces:**
- Consumes: `store.*`, `topology.Build`, `scheduler.ScanNow/Status`, `notify.Dispatcher.Test`.
- Produces: `func New(deps Deps) http.Handler` where `Deps{Store; Scheduler interface{ScanNow(string) error; Status() []scheduler.JobStatus}; Notifier interface{Test(ctx,string) error}; Config *config.Config}`.
  Routes (Go 1.22 mux):
  - `GET /healthz` → 200 `ok` (no auth)
  - `GET /` dashboard · `GET /map` · `GET /devices` · `GET /events`
  - `GET /api/networks` (name, cidr, device counts online/total, job status) · `GET /api/graph?network=` (Elements) · `GET /api/devices?network=` · `GET /api/events?network=&limit=&before=`
  - `PATCH /api/devices/{key}` JSON `{label?, known?}` → 204 / 404
  - `POST /api/scan/{network}` → 202 / 404 · `POST /api/notify/test/{channel}` → 204 / 404 / 502
  - Basic auth on everything except `/healthz` when `Config.Auth` set (constant-time compare).
  UI: server-rendered tables; map page loads `/api/graph` into Cytoscape (`cose` layout, routers as round-rectangles, devices colored green online / grey offline / orange unknown; click node → side panel with details and "mark known"/label form). Vendor `cytoscape@3.30.4` `dist/cytoscape.min.js` from jsdelivr into `static/`.
- [ ] **Step 1: Failing tests** with `httptest` + temp store: `TestHealthzNoAuth`; `TestAuthRequired` (401 without creds when Auth set); `TestPatchDevice` (204 then GET shows label); `TestPatchUnknown404`; `TestScanNow202`; `TestGraphJSON` (contains `"group":"nodes"`); `TestPagesRender` (each page 200 and contains its `<h1>`).
- [ ] **Step 2–4:** FAIL → implement → PASS.
- [ ] **Step 5:** Commit `feat(web): HTTP API, pages and topology map`.

### Task 12: Docker Compose deployment and end-to-end check

**Files:** Create `Dockerfile`, `docker-compose.yml`, `.dockerignore`, `.env.example`, `README.md`

- [ ] **Step 1:** `Dockerfile`: stage `golang:1.26-alpine` → `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /netmon ./cmd/netmon`; runtime `alpine:3.22` → `apk add --no-cache nmap ca-certificates tzdata libcap && setcap cap_net_raw,cap_net_admin+eip /usr/bin/nmap`; user `netmon` (uid 10001); `ENV NETMON_CONFIG=/config/config.yaml NETMON_LISTEN=0.0.0.0:8080`; `ENV NETMON_DATA_DIR=/data`; `HEALTHCHECK CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1`.
- [ ] **Step 2:** `docker-compose.yml` exactly as in the spec's Deployment section; `.env.example` lists `CORE_PASS`, `SNMP_COMMUNITY`, `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`, `DISCORD_WEBHOOK_URL`, `SMTP_PASSWORD`.
- [ ] **Step 3:** README: quickstart (copy `.env.example`, copy example config, `docker compose up -d`), MikroTik setup (`/user group add name=netmon-ro policy=read,api,!write,...`, `/ip service enable api-ssl`), SNMP setup (`/snmp set enabled=yes`, community), notifier setup.
- [ ] **Step 4: Verify**
  - `go vet ./... && go test ./...` → all PASS.
  - `docker compose build` → succeeds; with a config containing one nmap source on the LAN: `docker compose up -d && sleep 20 && docker compose ps` → `healthy`; `curl -s localhost:8080/healthz` → `ok`; `curl -s localhost:8080/api/devices | head` shows hosts.
  - `docker compose restart` → devices still returned (volume persists).
  - Open `/map` in the built-in browser → graph renders, no console errors.
- [ ] **Step 5:** Commit `feat: docker compose deployment and README`.
