# Map view: layouts, display options, ping distance — design

## Intent
The user (2026-11-23) wants the map clearer and slicker, with features switched on and off by checkboxes (e.g. connection port names), and devices placed at a distance from their router that reflects ping time.

## Decided by the user
- Ping times are measured **from the routers**.
- All three mocked-up layouts are available, chosen from a **dropdown**.

Everything else below is a proposal.

## Findings
- No round-trip times are stored. nmap's `srtt` is parsed away.
- From the netmon host, GTLan spans 0.1–237 ms, so a log scale is needed. ATLan times include the VPN (router alone 110–141 ms). Single samples jump (190 → 438 ms for one host).
- RouterOS `/ping` needs the `test` policy. Whether `netmon-ro` has it is unchecked.
- The RouterOS runner only does `Print(path)` and serializes calls on one connection, so pings need arguments and their own connection.

## Approaches
**Where to measure:** router (chosen by the user: true router→device distance, needs `test`) or netmon host (no router changes, but ATLan is mostly VPN noise).

**Layouts:** radar, radar with port sectors, distance lanes. All kept, because each reads best for a different question.

**Drawing:**
- *Keep Cytoscape with preset positions (recommended).* Keeps selection, panel, zoom and refresh. Rings, sectors and lanes go on a canvas layer underneath that follows pan and zoom.
- *Hand-written SVG.* Full control, but pan, zoom, selection and dragging must be rebuilt.
- *Cytoscape force layout with edge lengths.* Least code, but positions only approximate the distance and change on each run.

## Design

### Collecting
- Each MikroTik router referenced by a network's `router` source pings that network's online IPv4 devices every 5 minutes, 3 pings each, on a separate API connection, one device at a time. A run that is still going is not started twice.
- The run's result is the median of its answered pings. The stored value is the median of the last 5 runs. A device that did not answer the latest 3 runs has no current value.
- Stored per (network, device key, measuring origin): recent samples, smoothed ms, last answer time. Rows follow device key renames and merges, network renames, and device deletion, as device details do.
- Networks with no MikroTik router (VPN) use nmap's `srtt`, measured from netmon, smoothed the same way. Never mixed with router times in one network.
- If the router refuses `/ping` (no `test` policy), ARP polling is unaffected, the job shows the error, and the map says so. No silent switch to netmon times.
- Pings never change online/offline state and never raise alerts.

### Map data
- `/api/graph` device nodes gain, per network: `rttMs` (absent when unknown), `rttAt`, and the origin (router name or `netmon`).

### Layouts (dropdown: Radar, Port sectors, Distance lanes, Classic; default Classic)
- Classic is the original map (added by the user afterwards, and the default): no distances, so the scale options are disabled.
- One group per network, centred on its router; VPN is centred on a "netmon" marker, as its times are from netmon.
- Distance is logarithmic, 0.1–300 ms, clamped at both ends.
- Offline devices, and online ones without a ping time, sit on the dashed outer ring (end strip in lanes). Online ones keep their online color.
- Radar: even angles in a stable order, so a device keeps its direction as its distance changes. Sectors: a wedge per router port, sized by device count, with a minimum width. Lanes: one per port. Devices without a port share "no port".
- A machine in several networks is drawn once, in its transit network as now, with links to the others. It uses that network's time.
- Lines between networks are not to scale.
- Labels that would overlap are hidden until zoom or hover. The router's own name always shows.
- The 60 s refresh moves devices smoothly and keeps pan, zoom and selection.

### Display options (toolbar popover)
| Option | Default |
|---|---|
| Device names | on |
| IP addresses | off |
| Ping times | off |
| Port names | off |
| Distance rings/scale | on |
| Connection lines | on |
| Offline devices | on |
| Distance by ping | on |

With "Distance by ping" off, devices sit on one even ring (evenly spaced in lanes) and the scale is hidden. Layout and options are remembered per browser; the map works without browser storage.

### Look
Existing colors and light/dark themes, thin lines, quieter labels. The side panel gains ping time, where it was measured from, and when.

## Non-goals
Ping history charts, latency alerts, pinging from SNMP-only routers, IPv6 pings, saved manual positions.

## Failure even if it works
A map that jumps around on refresh; times from different origins presented as comparable; ping load that delays ARP polling.

## Testing
Go: RouterOS time parsing (`1ms234us`, whole-ms v6 values, timeouts), smoothing, storage across renames/merges, refused permission, graph data. Layout math is kept in plain functions with tests. Browser check of every layout and option in light and dark, with sample data.

## Left to the builder
Table and field names, angle hashing, label collision method, popover markup.
