# Cleanup, multi-IP machines, network links, scan jobs — design

## Intent
The user (2026-11-22) runs two LANs (GTLan 192.168.8.0/24 where netmon lives, ATLan 192.168.88.0/24) joined by a VPN (10.0.10.0/24). They want:
1. **Cleanup.** Renaming `lan` → `GTLan` left 26 orphan `lan` devices that clutter the map. Renames should carry data over, and stale data should be removable.
2. **One machine, several IPs.** A machine found in several networks should be one entry with all its IPs.
3. **See how the three networks connect** on the map.
4. **See the scan jobs**: what is running, for how long, progress, and when each runs next.

## Findings that shape it
- Device rows are keyed by `(network, key)`, so all data hangs off the network *name*.
- Only GTLan yields MACs. ATLan and VPN hosts are reached through the router, so they're bare IPs; nothing links 10.0.10.x to 192.168.88.1.
- `nmap -sn --traceroute` works in the container and gives the path: `192.168.8.1 → 10.0.10.1 → 192.168.88.1` (10.0.10.1 is the VPN hub; 10.0.10.6 is also reached via it).

## Approaches (identity of multi-IP machines)
- MikroTik router API: exact (routers report all their own addresses), but needs router access set up. Not chosen.
- **Traceroute + manual merge (chosen by the user):** nmap traceroute shows which hops join the networks; the user merges IPs that are the same machine. Automatic grouping only where it is certain (same MAC in two networks).
- Hostname matching: unreliable (names mostly empty off-LAN). Not used.

## Design

### 1. Cleanup
- **Rename carries data.** `Network` gets `OriginalName` (as routers/channels have). When a save renames a network, all its rows (devices, events, links, network_state, machine membership) move to the new name in one transaction. If the new name already has a device with the same key, the existing (newer) row wins; label/known from the old row fill in where empty.
- **Stale data** section on the Settings page: lists network names that have stored data but are not in the config, with device/event counts. Actions: **Merge into…** (a configured network, same rules as a rename) or **Delete** (devices, links, state; events kept, since history is history — *my call*). Both need auth, like other edits.
- **Forget device**: a delete button per device on the devices page (it comes back if seen again).
- For the user right now: delete `lan` (GTLan already has every device).

### 2. Machines (one device, several IPs)
- New tables: `machines(id, label)` and `machine_members(network, key, machine_id)`. Per-network device rows are unchanged, so scanning, diffing and offline detection stay per network.
- **Automatic:** device rows in different networks with the same MAC key are grouped.
- **Manual:** on the devices page, select rows → **Merge into one machine**; on a machine, **Split** removes a member. Label/known on a machine apply to all its members.
- Membership follows key renames (IP-only device gains a MAC) and ghost-row merges already done by the store.
- Devices page shows a machine as one row listing every network/IP; map shows one node.

### 3. Network connections
- nmap sources get `traceroute: true` (per source, off by default; Settings shows a checkbox). nmap's `--traceroute` hops are stored as links of kind `hop` between consecutive hop IPs. The last hop into an ordinary host is dropped (that host already hangs off its network), so links describe the routers between networks.
- **Map "All networks" view** (new default when several networks exist):
  - each network is a box (Cytoscape compound node) with its devices;
  - a machine in several networks sits between their boxes with an edge to each box;
  - hop links draw router→router edges; a hop IP not in any monitored network is a small grey "hop" node;
  - the existing per-network view stays (network picker).
- Sketch for this LAN, after merging the routers' VPN addresses:
  ```
  [GTLan ...hosts] — GT router (192.168.8.1, 10.0.10.x) — [VPN: 10.0.10.1 hub, ...] — AT router (10.0.10.6?, 192.168.88.1) — [ATLan ...hosts]
  ```
  Before merging, traceroute alone already shows GTLan → 10.0.10.1 → ATLan.

### 4. Scan jobs
- Scheduler status gains `state` (idle/queued/running), `startedAt`, `nextRun`, `progress` (0–100, or absent when unknown), `lastDuration`.
- Progress: nmap runs with `--stats-every 2s`; its streamed `<taskprogress percent=…>` is parsed while the scan runs and reported through a progress callback passed in the context. Router/SNMP report none (spinner).
- **Jobs panel** on the Overview page (and `GET /api/jobs`), refreshed every 2 s: per job the network, source, state, elapsed timer + progress bar while running, "queued" while waiting for a free slot (max 4 concurrent), countdown to next run, last duration, last error, and a **Scan now** button per job.

## Non-goals
- Router API changes, MikroTik `/ip/address` (can come later and slot into machines).
- Automatic merging by hostname or by traceroute alone.
- Progress for router/SNMP sources.
- Event retention/pruning.

## Left to the builder
Table/column names, API shapes, Cytoscape layout choice for compound nodes (fcose/cose vs. a fixed left-to-right layout), how often the jobs panel polls (≈2 s).
