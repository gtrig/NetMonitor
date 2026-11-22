# Scan profiles (quick + detailed) — design

## Intent
The user (2026-11-22) wants two kinds of scans: a quick one for presence (what exists today) and a longer one for service discovery and host details. **Inventory only** (user's choice): results are shown, never diffed or notified.

## Approaches
- Extend the quick nmap source with port flags: would slow presence scans and mix inventory into offline detection. Rejected.
- **A separate `services` source per network (chosen):** its own long interval, scans only hosts that are currently online, stores the latest result per device.

## Design
- **Config:** source `{type: services, interval: 6h, args: [...]}`; default interval 6h; no `ref`, no `traceroute`; any extra nmap args allowed (port/OS/version bans stay on `nmap` sources only). At most one services source per network.
- **Scan:** `nmap -sV -O -Pn -v -oX - --stats-every 2s <args> <online IPs of the network>` (top 1000 TCP ports by default). Same pty/progress handling as quick scans; same 4 concurrent slots; shows in the jobs panel.
- **Stored per device row** (table `device_details`, latest result only): open ports (protocol, port, service, product, version, extra info), OS guess + accuracy, scanned-at. Follows key renames, network renames/merges, forget and delete like other per-device data.
- **Not part of presence:** services jobs never mark devices online/offline, never create events, never count for offline thresholds.
- **On demand:** "Scan details now" for one device (map panel and devices page) runs a services scan for that IP with the network's services args (or defaults when the network has none), shown in the jobs panel while running.
- **UI:** devices page "Services" column with a summary (`22 ssh, 80 http`) expandable to the full table and OS; map panel lists OS and ports per address (all members of a machine); settings source picker gets `services`.

## Non-goals
NSE/vulnerability scripts, UDP, history, change alerts.

## Left to the builder (my calls)
Summary formatting, table/column names, API shapes.
