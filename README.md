# netmon

A small self-hosted network monitor for a home lab. It discovers the devices on your networks, draws them as a topology map in the browser, and notifies you via **Telegram**, **Discord** or **email** when something changes.

Each network can use any mix of discovery sources, each on its own interval:

| Source   | How                                       | Gives you |
|----------|-------------------------------------------|-----------|
| `router` | MikroTik RouterOS API (polled, read-only) | ARP table, DHCP hostnames, bridge port per device, router↔router links (`/ip/neighbor`) |
| `snmp`   | SNMP v2c/v3                               | ARP table, bridge forwarding table (MAC → port), LLDP neighbors |
| `ping`   | Concurrent ICMP sweeps from netmon (IPv4, up to /16; default every 10s) | Who is online within seconds, MAC on directly attached networks |
| `nmap`   | `nmap -sn` host discovery (never port-scans) | Live hosts, MAC + vendor on directly attached networks |
| `services` | `nmap -sV -O` of the hosts that are online (slow; default every 6h) | Open TCP ports, service versions, OS guess (inventory only: no alerts) |

Detected changes: `device_new`, `device_offline`, `device_back_online`, `ip_changed`, `hostname_changed`, `mac_changed_for_ip` (possible spoofing), and a single `baseline` summary the first time a source sees a network.

## Quick start (Docker Compose)

```bash
cp .env.example .env
cp config/config.example.yaml config/config.yaml
# set NETMON_UID/NETMON_GID in .env to the owner of ./config (id -u; id -g),
# add an auth: block to config/config.yaml, then edit the rest here or on the Settings page
docker compose up -d
```

Open `http://<host>:8080`. Data (SQLite) lives in the `netmon-data` volume and survives restarts and upgrades (`docker compose up -d --build`).

The container uses `network_mode: host` and the `NET_RAW`/`NET_ADMIN` capabilities so nmap can do ARP discovery on your LAN. The web UI has no authentication by default: set `auth:` in the config if the host is reachable by others.

## Settings page

Networks, routers, SNMP targets, notification channels and rules can be managed on the **Settings** page, under the **Networks**, **Data sources** and **Notifications** tabs; one **Save changes** covers them all. **System** has the scan workers, the file reload and leftover data. Saving writes `config/config.yaml` and applies the change immediately (no restart); devices and history are kept.

- **Editing requires `auth:`** in the config (otherwise the page is read-only), since settings control where alerts go.
- `listen`, `data_dir` and `auth` are only changed in the file (restart to apply), so a UI change can't lock you out.
- **Secrets** are never sent to the browser. A field shows whether it is set or reads from `.env` (`${NAME}`). Leave it empty to keep the current value; type `${NAME}` to keep a secret in `.env`, or type the value itself to store it in the file.
- Invalid changes are rejected with every problem listed, and nothing is written.
- Hand edits to the file are fine: use **Reload from file**, or `docker compose kill -s HUP netmon`. An invalid file is reported and the running configuration is kept.
- Saving from the page rewrites the file, so comments in it are lost.

The container runs as the host user that owns `./config` (`NETMON_UID`/`NETMON_GID` in `.env`, default 1000) so it can write the file.

## Several networks, machines and cleanup

- **All-networks map.** With more than one network, the map's default view draws each network as a box. Pick a network to see it alone.
- **Map layouts.** **Classic** (the default) is the plain topology view with a box per network. **Radar** draws each network around its router, **Port sectors** groups devices by router port, **Distance lanes** puts the router on the left. **Display** switches names, IPs, ping times, port names, rings, lines and offline devices on and off. Both choices are remembered in the browser.
- **Distance is ping time.** Every 5 minutes each MikroTik router pings the online devices of its network, and except in Classic, devices sit further out the slower they answer (log scale, 0.1–300 ms). The router user needs the RouterOS `test` policy for this; without it the map says so and everything else keeps working. Networks without a MikroTik router use the times the `ping` source, or else nmap, measures from netmon. Pings never affect online/offline state or alerts.
- **Ping history.** A device's page graphs its pings over 1 hour to 30 days and lists every one; history is kept for 30 days.
- **Traceroute.** Tick **Traceroute** on an nmap source (or set `traceroute: true`). nmap then records the routers on the path to each host, which draws how your networks connect, e.g. LAN → VPN hub → remote LAN. Hop addresses that netmon doesn't otherwise know show as small grey diamonds.
- **Machines.** One box with addresses in several networks (a router, a VPN endpoint) is found once per network. On the **Devices** page, tick its rows and click **Merge selected into one machine**. The map then shows it as one node with all its addresses, sitting between the networks it joins. **Split** undoes it. The same MAC address seen in two networks is drawn as one node automatically.
- **Renames keep data.** Renaming a network on the Settings page moves its devices and history to the new name.
- **Cleanup.** Data under network names that are no longer configured is listed under **System** on the Settings page: **Merge into** a configured network, or **Delete** it (events are kept). **Forget** on the Devices page removes a single device; it comes back if it is seen again.
- **Jobs.** The **Jobs** page shows every scan job live: running (with nmap's phase and percentage), queued (with its place in the queue) or idle (with a countdown to the next run), plus the last duration and error, and **Scan now** per job. Remote networks usually spend most of a scan on reverse DNS; add `-n` to the nmap args to skip it if you don't need names. **History** below it lists every finished job for 30 days: when, what started it (manual, scheduled, automatic), how long it took, its outcome (OK, failed, cancelled, skipped, interrupted) and what it found or the error, with filters for network, kind and outcome.

## Quick and detailed scans

- **Quick** (`nmap`, `router`, `snmp` sources, and `ping`) answers "who is online" every few minutes and drives offline detection and alerts.
- **Queue.** Scans wait in one queue: manual ones first, then quick scans, scheduled detailed scans, and automatic detailed scans. A scan is never queued twice. **Scan queue** on the Jobs page shows what runs and waits, and cancels waiting scans. Router pings do not queue. `workers: {scans: 4, detailed: 2}` (Settings → System) sets how many scans run in parallel and how many of those may be detailed; `detailed` must be lower, so quick scans are never held up by slow ones.
- **Automatic detailed scans.** After each quick scan, online devices that have no detailed result yet are queued for one, also in networks without a services source. A failed device is retried after 1h, then at doubling intervals up to 24h; cancelling a device's scan postpones it for 24h. Retry times reset on a restart or settings save. Switch it off per network with `auto_details: false` or on the Settings page.
- **Detailed** (`services` source, one per network, default every `6h`) runs `nmap -sV -O` on the top 1000 TCP ports of each host that is currently online, one queue entry per device, and keeps the latest result per device: open ports, service product/version, OS guess. It is inventory only: it never marks devices online/offline and never sends alerts. Expect it to be slow (about 15 minutes for 25 home/IoT devices). Extra nmap args are allowed, e.g. `-p 1-65535` or `--version-light`; NSE scripts (`--script`, `-sC`, `-A`), other target lists (`-i…`) and output files (`-o…`) are rejected. **Scan now** on a network refreshes presence only.
- Results show in the **Services** column on the Devices page and in the device panel on the map. **Scan details** / **Rescan** puts one device at the front of the queue (also in networks without a services source); it shows on the Jobs page until it is done.

## Fast presence (ping)

A `ping` source (one per network, default every `10s`, at least `2s`) pings every address of the network and notices devices that come, go or sleep. Keep another source next to it for devices that block ping.

- **Offline:** a device that answered ping and then misses 3 sweeps (about 30–40s) gets a last check (more pings, TCP ports 22, 80, 443 and 445, and on attached networks whether ARP confirmed it since). Without any sign of life it is offline, whatever other sources last saw. Router and SNMP tables, which keep devices that left, cannot bring it back; a ping reply or a newer nmap scan can. Devices nmap sees but that stopped answering ping follow the usual offline rules again. A firewall that answers TCP on behalf of absent hosts keeps them online.
- **Sleepers:** netmon learns how long a device is usually silent (5 similar gaps within 7 days, each up to 2h) and waits 1.5 times its usual longest one. Meanwhile it shows as **asleep**, without alerts. Until it is learned, a sleeper alerts; set **Offline after** on its device page to choose the time yourself.
- **Broken paths:** if a sweep fails, all devices go silent at once, or more than half of at least 6, nothing is marked offline until they answer again, or for 5 minutes. A sweep that fails (no network, no permission) never marks anything offline.
- `offline_after` on the network is the minimum for all its devices. Sweeps run outside the scan queue; the job history shows only manual ones, failures and recoveries. Without a MikroTik router, ping times are recorded every 5 minutes.
- It needs no privileges where `net.ipv4.ping_group_range` covers netmon's group (the default on most distributions and in Docker), otherwise `CAP_NET_RAW`.

## Wake on LAN

**Wake** on the Devices page (one row or all selected ones), a device's page or the map panel sends a Wake-on-LAN packet. It needs the device's MAC address, and Wake on LAN enabled on the device itself. The packet has no reply: the next scan shows whether the device woke up.

- **Networks with a `router` source**: that MikroTik router (the first one listed) sends it with `/tool wol`, on its interface in the network's `cidr`, so networks behind a VPN work too. Its user needs the `test` policy. If the router fails, the error is shown and nothing else is tried.
- **Other networks**: netmon broadcasts it itself (UDP port 9). That only works on networks the netmon host is attached to, and `cidr` must match the real subnet.

## Configuration

Everything lives in `config/config.yaml`; `${VAR}` references are filled from the environment (`.env`). See [config/config.example.yaml](config/config.example.yaml) for a complete example.

- **networks**: `name`, `cidr`, optional `exclude` (IPs to ignore) and `sources`. Hosts outside the CIDR are ignored, so one router can feed several networks.
- **sources**: `{type: ping|router|snmp|nmap, ref: <router or snmp target name>, interval: 1m}`. Default interval `5m`. Extra nmap flags go in `args` (port/OS/version scan flags are rejected); `traceroute: true` (nmap only) maps the routers in between.
- **Offline detection**: a device is reported offline once *every* source that has seen it has missed it on 3 consecutive successful collections (override with `offline_after: 30m` per network). Failed collections (router unreachable, wrong password) never mark devices offline.
- **channels**: `telegram` (`bot_token`, `chat_id`), `discord` (`webhook_url`), `email` (`smtp_host`, `smtp_port`, `starttls`, `username`, `password`, `from`, `to`). Port 465 uses implicit TLS.
- **rules**: which `events` (empty = all), for which `networks` (empty = all), go to which `channels`. `only_unknown: true` skips devices you've marked as known in the UI. Changes from one collection are batched into one message per channel.

Environment overrides: `NETMON_CONFIG` (config path), `NETMON_LISTEN`, `NETMON_DATA_DIR`, `NETMON_DEBUG`, `NETMON_DEBUG_TOKEN`.

## MikroTik setup

Create an API user without write access and enable the API (TLS recommended):

```
/user group add name=netmon policy=read,api,test,!write,!policy,!sensitive,!ftp,!ssh,!telnet,!winbox,!web,!reboot,!password,!sniff,!romon,!rest-api
/user add name=netmon-ro group=netmon password=CHANGE-ME
/ip service set api-ssl disabled=no address=<monitor-ip>/32
```

`test` lets netmon ping from the router and send Wake-on-LAN packets; use `!test` if you want neither.

Use port `8729` with `tls: true` (add `insecure_skip_verify: true` for the default self-signed certificate) or `8728` with `tls: false`. Add one `routers:` entry per MikroTik. Neighbor discovery (`/ip neighbor discovery-settings`) gives you the link between your two routers on the map.

Optional SNMP: `/snmp set enabled=yes` and configure a community restricted to the monitor's address.

## Telegram / Discord

- **Telegram**: create a bot with @BotFather and get its token. Send the bot a message, then read your chat id from `https://api.telegram.org/bot<token>/getUpdates`.
- **Discord**: Server settings → Integrations → Webhooks → New webhook → copy the URL.

Use **Send test** on the Overview page to check each channel.

## netping

A standalone, concurrent ping sweeper, for use on its own; netmon's `ping` source uses the same package:

```bash
go run ./cmd/netping 192.168.8.12                        # one address
go run ./cmd/netping 192.168.8.1/24                      # a network (or /255.255.255.0)
go run ./cmd/netping -count 5 192.168.8.1/24 192.168.88.1/24
```

It lists the hosts that answered, with their minimum, maximum and average response times (`-count` pings per host, default 3; `-json` for JSON; flags go first). A /24 takes about 1.8s with 3 pings per host. It needs no root on Linux if `net.ipv4.ping_group_range` covers your group, else `CAP_NET_RAW`. Hosts that block ICMP are not found; networks larger than /16 are rejected. The Go package is `pinger`.

## Debug mode (MCP)

`netmon -debug` (or `NETMON_DEBUG=1`, also in `.env` for Docker) serves an [MCP](https://modelcontextprotocol.io) server at `/mcp`, port 8099, so that an assistant such as Claude Code can look into the running netmon: status, devices, ping sweep state, events, job history, logs (including debug lines), config, read-only SQL, web pages and goroutines. It can also `ping` addresses of configured networks and start scans; it cannot change anything else.

Two things protect it:

- **Allowed networks.** Without `allow`, only this machine can connect. List every network that may, including this machine if wanted; changes apply on reload, unless they change the address it listens on:

  ```yaml
  debug:
    allow: [127.0.0.0/8, 192.168.8.0/24]
    listen: 192.168.8.5:8099   # optional; default: every address if allow has other networks
  ```
- **A token.** It is created in `data_dir/debug-token`, or set `NETMON_DEBUG_TOKEN` (at least 16 characters).

Connect Claude Code, then start a new session:

```bash
claude mcp add --transport http netmon http://127.0.0.1:8099/mcp --header "Authorization: Bearer $(cat data/debug-token)"
```

Without the `claude` command, put this in `.mcp.json` in the project's folder (git-ignored, as it holds the token):

```json
{"mcpServers": {"netmon": {"type": "http", "url": "http://127.0.0.1:8099/mcp", "headers": {"Authorization": "Bearer TOKEN"}}}}
```

Configured secrets are masked in all answers. Traffic is plain HTTP, so anyone who can read it on the way sees the token and the answers: allow only networks you trust, or use a VPN or SSH tunnel. netmon checks the address a connection comes from; behind a proxy that is the proxy's. With Docker bridge networking it can be the bridge's (compose uses host networking).

## Development

```bash
go test ./...
go build -o netmon ./cmd/netmon && ./netmon -config config/config.yaml
npm ci && npm run css        # only after changing UI markup or styles (Tailwind CSS v4)
```

Running locally without root, nmap falls back to TCP pings and finds no MAC addresses. Run as root or `sudo setcap cap_net_raw,cap_net_admin+eip $(which nmap)` with `NMAP_PRIVILEGED=1` for ARP discovery.
