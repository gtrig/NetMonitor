# Scan queue and automatic detailed scans

Approved in conversation, 2026-11-23.

**Why.** Build a better picture of the network without manual work, without slow scans delaying presence checks.

**Queue.** One in-memory priority queue: manual, presence, scheduled detailed, automatic detailed; first come first served within a priority. A queued scan is never queued twice; asking for it manually moves it up. Pings do not queue. The Jobs page shows it with positions and cancels waiting entries. Cancelling a device scan also postpones automatic scans of that device for 24h.

**Workers.** `workers: {scans: 4, detailed: 2}` (file, or Settings → System). `detailed` must be lower than `scans`, so one worker is always free for presence. Applies on save, like other settings.

**Detailed scans.** One entry per device. A `services` job queues its network's online devices and waits for them without holding a worker. A scheduled run skips devices scanned within the last half interval, so an interrupted run resumes instead of starting over.

**Automatic.** After each successful presence collection, online IPv4 devices with no stored result are queued. Failures retry after 1h, doubling up to 24h (in memory: a restart or settings save retries at once). On by default; `auto_details: false` per network.

**Restart or save.** Automatic and scheduled work is found again from stored results; waiting manual scans are dropped.

**Approaches.** (1) In-memory priority queue — picked. (2) Only make the slot count configurable: no order or visibility. (3) Queue stored in SQLite: more cleanup, while nearly all entries can be rebuilt.
