# Device ping history — design (approved by the user 2026-11-23)

**Intent:** see a device's ping history as a list and as a graph. Kept for **30 days** (the user's choice).

- **Stored:** every raw run result (router and nmap times) in `ping_history`; follows renames and merges, deleted with the device or network, pruned daily. Failed runs and offline devices record nothing.
- **Device page:** a "Ping history" section per address of the machine, since times from different origins don't compare.
- **Graph:** 1 h, 24 h (default), 7 d, 30 d. Raw up to 24 h; 7 d in 30 min and 30 d in 2 h buckets: median line with a min–max band. Missed pings are red marks and break the line; missing data is a plain gap. Hover or arrow keys show time and value. Linear, with a log switch.
- **Summary:** median, min, max and % answered over the range's raw runs.
- **List:** newest first: time, result, origin; 50 per page.
- **Map panel:** a small 24 h graph per address, linking to the device page.

**Approaches:** one SQLite table and hand-drawn SVG (chosen); a chart library (size for one graph); Prometheus or similar (another service).
**Non-goals:** latency alerts, longer retention, export.
