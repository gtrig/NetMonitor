# Ping presence — design (the user asked to implement it 2026-11-25)

**Intent (the user's):** a native Go pinger, in addition to nmap, that pings subnets concurrently and reports active hosts. It only does presence, so it can be considerably faster than nmap. Devices that show up periodically (awake every 10 minutes, then asleep) must be detected and their interval learned and allowed for.

**Goals:** new and returning devices within about one sweep (10s); always-on devices offline in about 30–40s; few false alerts for sleepers.
**Non-goals:** IPv6, replacing nmap, ports/services, traceroute, passive sniffing.
**Anti-goals:** alert floods when netmon's own path (VPN) breaks; a database that grows with every sweep; delaying other scans.

Everything marked *(call)* is a proposal, not something the user decided.

## Approaches

1. **Ping, then confirm** *(call; stated to the user as the default, no objection)*: missed sweeps trigger a short targeted check, then offline. Nearly as fast as 2, fewer false alarms.
2. **Ping decides alone:** simplest; packet loss and newly blocked ICMP cause false alerts.
3. **Separate "unresponsive" state, alerts as today:** no new false alerts, but alerts stay slow.

Per sweep, running through the existing scan queue was rejected: each 10s sweep would wait behind nmap, write a `job_runs` row (8,640 per day per network, kept 30 days) and a ping history row per device. SQLite has one connection.

## Pinger (`pinger/`, `cmd/netping`) — built standalone at the user's request

- Standard library only; imports nothing from netmon. `cmd/netping` runs it on its own.
- ICMP echo over unprivileged datagram sockets, raw sockets as fallback. If neither opens, the error carries the `ping_group_range` hint.
- One paced sender over several sockets, one receiver goroutine per socket, replies matched by address, index, round and a random token. 1,000 packets/s by default.
- Called with any number of addresses and networks with a netmask (the user's interface), plus `-count`, default 3: every host gets that many pings, at least 200ms apart, then one 1s wait for late replies. Returns the active addresses with min, max and average response time, and replies/sent.
- Several sockets because packets waiting for ARP (about 3s per silent local address) fill a socket's send buffer after about 250 packets and block the sender: a /24 took 8.4s on one socket, 2.5s on four (two retried attempts, the first version).
- Measured on an attached /24 (2026-11-25): 1.25s for 1 ping per host, 1.76s for 3, 3.54s for 10. `nmap -sn -n` without root took 2.4–14.4s on the same network.
- Skips network/broadcast addresses and `exclude`. Networks larger than /16 are rejected.

## Scheduling

- New source type `ping`: at most one per network, default interval `10s`, minimum `2s`, no args. Available in Settings. A sweep never overlaps itself; if one runs long, the next starts after it.
- Its own loop, outside the scan queue. Live status shows on the Jobs page. History gets a row only for manual runs, failures and recoveries.
- Sweep state is held in memory. The database is written only for new devices, online/offline changes and ended gaps; `last_seen` is flushed once a minute. Ping time history is stored every 5 minutes, only where no router pings the network.
- Devices and events go through `diff.Apply`. When several rows share an IP, a MAC-less reply goes to the most recently seen one. MACs are read from the kernel's neighbor table on attached networks only; behind a router or VPN a new host is keyed by IP, and attaches to a known MAC device with that IP.

## Going offline

- Ping governs a device once it has answered ping. Others keep today's rules.
- Past its limit (3 sweeps, or its learned limit), a confirm check runs: 3 pings 300ms apart, and TCP connects to ports 22, 80, 443 and 445, where an accept or a refusal from the host itself means it is alive. On attached networks, an ARP entry confirmed since the last reply counts too (read by netlink: the kernel lists hosts as complete for up to ~45s after they leave). This works across a VPN, where ARP does not.
- Nothing answers: offline, regardless of an older sighting by another source. Router and SNMP tables cannot bring it back; a ping reply or a later nmap sighting can.
- Alive by TCP or ARP: its silence starts over and is checked again later. Seen by nmap but silent to ping: ICMP is blocked, so the device returns to today's rules until it answers ping again.
- **Broken path:** if a sweep errors, or gets no reply after at least 2 governed devices answered the one before, or more than half of at least 6 online governed devices vanish in one sweep, offline marking is held. It resumes once replies return. After 5 minutes it resumes anyway, so a real power cut is reported, as one batch *(call)*.

## Learned silence

- A gap runs from a device's last sighting to its next, counted if at least 3 sweeps were missed (shorter ones never change the limit). Ping and nmap sightings end gaps; router and SNMP ones do not. Time when netmon was stopped, sweeps failed or marking was held is discarded.
- Table `presence_gaps` (network, key, ended_at, seconds), 7 days, at most 100 per device; gaps over 2h are not stored. Rows follow renames, merges and forgets like pings do. Gaps are recorded after going offline too, which is how learning starts.
- **Periodic:** at least 5 gaps, 70% of them within 25% of the median (10s minimum tolerance).
- **Limit:** 1.5 × the 95th percentile of the gaps near the median, at least 3 sweeps. Non-periodic devices: 3 sweeps. A network's explicit `offline_after` is the minimum for all its devices.
- **Override:** per device, "offline after" duration; empty = automatic. Stored with label and known, in table column `offline_after`.
- **Display** *(call)*: a periodic device past 3 sweeps but within its limit shows as **asleep** (badge on Devices, device page and map; no alerts; still stored online). The device page shows the pattern, e.g. "every ~10m (±1m), offline after 16m 30s", and the override field.

**Known limits:** a new sleeper alerts until 5 gaps are learned, unless overridden. A device awake for less than a sweep interval can be missed. Real failures of sleepers take as long as their limit to show.

## Testing

Unit: packet matching (late, duplicate, foreign replies), pacing and cancellation through a fake socket; gap classification; limits; confirm; broken path. Store: gaps and override survive restart, rename, merge, forget. Config validation, Settings and web rendering. An opt-in loopback test of a real socket, and a timed sweep of a real /24 compared with `nmap -sn`. The Docker image's unprivileged socket is checked as its user (10001), and by compose's host networking.

## Left to the builder

File layout, names, fake-clock approach, badge styling within the existing design.
