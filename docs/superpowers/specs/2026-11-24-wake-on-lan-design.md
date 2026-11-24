# Wake on LAN — design (approved by the user 2026-11-24)

**Intent:** wake a sleeping device from the UI.

- **Sending:** netmon broadcasts the magic packet to the network's directed broadcast address, UDP port 9.
- **API:** `POST /api/devices/wake` with network and key. The MAC comes from the store and the network from the config, never from the browser. No `auth:` needed, like scanning.
- **UI:** Wake in the Devices rows and bulk bar, the device page and the map panel; only where a MAC is known. It reports "Wake packet sent", since there is no reply.

**Approaches:** netmon sends it (the user's choice: routers stay read-only); also `/tool wol` on MikroTik (reaches remote networks, but needs more than the read-only API policy).
**Non-goals:** waking through routers, schedules, confirming that the device woke.
