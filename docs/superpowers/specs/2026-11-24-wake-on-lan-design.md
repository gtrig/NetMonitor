# Wake on LAN — design (approved by the user 2026-11-24)

**Intent:** wake a sleeping device from the UI.

- **Sending:** netmon broadcasts the magic packet to the network's directed broadcast address, UDP port 9.
- **API:** `POST /api/devices/wake` with network and key. The MAC comes from the store and the network from the config, never from the browser. No `auth:` needed, like scanning.
- **UI:** Wake in the Devices rows and bulk bar, the device page and the map panel; only where a MAC is known. It reports "Wake packet sent", since there is no reply.

**Approaches:** netmon sends it (the user's choice: routers stay read-only); also `/tool wol` on MikroTik (reaches remote networks, but needs more than the read-only API policy).
**Non-goals:** waking through routers, schedules, confirming that the device woke.

## Revision: through MikroTik routers (approved by the user 2026-11-24)

**Why:** the deployed netmon is behind a VPN, where its broadcasts never reach the LANs.

- Networks with a MikroTik source wake through their first router: `/tool/wol` with the stored MAC, on the interface found in `/ip/address`. Needs `test`, no `write`. Others keep the broadcast.
- A failing router is reported, with no fallback to a broadcast. The message names the sender ("via ATHome").
- This replaces "netmon sends it" and the "waking through routers" non-goal above. Not covered: devices without a MAC (routed VPN).
