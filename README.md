# netmon

A small self-hosted network monitor for a home lab. It discovers the devices on your networks, draws them as a topology map in the browser, and notifies you via **Telegram**, **Discord** or **email** when something changes.

Each network can use any mix of discovery sources, each on its own interval:

| Source   | How                                       | Gives you |
|----------|-------------------------------------------|-----------|
| `router` | MikroTik RouterOS API (polled, read-only) | ARP table, DHCP hostnames, bridge port per device, router↔router links (`/ip/neighbor`) |
| `snmp`   | SNMP v2c/v3                               | ARP table, bridge forwarding table (MAC → port), LLDP neighbors |
| `nmap`   | `nmap -sn` host discovery (never port-scans) | Live hosts, MAC + vendor on directly attached networks |

Detected changes: `device_new`, `device_offline`, `device_back_online`, `ip_changed`, `hostname_changed`, `mac_changed_for_ip` (possible spoofing), and a single `baseline` summary the first time a source sees a network.

## Quick start (Docker Compose)

```bash
cp .env.example .env
cp config/config.example.yaml config/config.yaml
# edit config/config.yaml (networks, routers, channels) and .env (secrets)
docker compose up -d
```

Open `http://<host>:8080`. Data (SQLite) lives in the `netmon-data` volume and survives restarts and upgrades (`docker compose up -d --build`).

The container uses `network_mode: host` and the `NET_RAW`/`NET_ADMIN` capabilities so nmap can do ARP discovery on your LAN. The web UI has no authentication by default: set `auth:` in the config if the host is reachable by others.

## Configuration

Everything lives in `config/config.yaml`; `${VAR}` references are filled from the environment (`.env`). See [config/config.example.yaml](config/config.example.yaml) for a complete example.

- **networks**: `name`, `cidr`, optional `exclude` (IPs to ignore) and `sources`. Hosts outside the CIDR are ignored, so one router can feed several networks.
- **sources**: `{type: router|snmp|nmap, ref: <router or snmp target name>, interval: 1m}`. Default interval `5m`. Extra nmap flags go in `args` (port/OS/version scan flags are rejected).
- **Offline detection**: a device is reported offline once *every* source that has seen it has missed it on 3 consecutive successful collections (override with `offline_after: 30m` per network). Failed collections (router unreachable, wrong password) never mark devices offline.
- **channels**: `telegram` (`bot_token`, `chat_id`), `discord` (`webhook_url`), `email` (`smtp_host`, `smtp_port`, `starttls`, `username`, `password`, `from`, `to`). Port 465 uses implicit TLS.
- **rules**: which `events` (empty = all), for which `networks` (empty = all), go to which `channels`. `only_unknown: true` skips devices you've marked as known in the UI. Changes from one collection are batched into one message per channel.

Environment overrides: `NETMON_CONFIG` (config path), `NETMON_LISTEN`, `NETMON_DATA_DIR`.

## MikroTik setup

Create a read-only API user and enable the API (TLS recommended):

```
/user group add name=netmon policy=read,api,!write,!policy,!sensitive,!test,!ftp,!ssh,!telnet,!winbox,!web,!reboot,!password,!sniff,!romon,!rest-api
/user add name=netmon-ro group=netmon password=CHANGE-ME
/ip service set api-ssl disabled=no address=<monitor-ip>/32
```

Use port `8729` with `tls: true` (add `insecure_skip_verify: true` for the default self-signed certificate) or `8728` with `tls: false`. Add one `routers:` entry per MikroTik. Neighbor discovery (`/ip neighbor discovery-settings`) gives you the link between your two routers on the map.

Optional SNMP: `/snmp set enabled=yes` and configure a community restricted to the monitor's address.

## Telegram / Discord

- **Telegram**: create a bot with @BotFather and get its token. Send the bot a message, then read your chat id from `https://api.telegram.org/bot<token>/getUpdates`.
- **Discord**: Server settings → Integrations → Webhooks → New webhook → copy the URL.

Use **Send test** on the Overview page to check each channel.

## Development

```bash
go test ./...
go build -o netmon ./cmd/netmon && ./netmon -config config/config.yaml
```

Running locally without root, nmap falls back to TCP pings and finds no MAC addresses. Run as root or `sudo setcap cap_net_raw,cap_net_admin+eip $(which nmap)` with `NMAP_PRIVILEGED=1` for ARP discovery.
