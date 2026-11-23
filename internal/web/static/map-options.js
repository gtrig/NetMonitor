// What the map shows, remembered per browser.
(function (root) {
  const KEY = "netmon.map.v1";
  const LAYOUTS = ["radar", "sectors", "lanes"];
  const DEFAULTS = { layout: "radar", names: true, ips: false, ms: false, ports: false, rings: true, lines: true, offline: true, distance: true };

  function load(storage) {
    const out = { ...DEFAULTS };
    let saved;
    try { saved = JSON.parse(storage.getItem(KEY)); } catch { return out; } // blocked or unreadable
    if (!saved || typeof saved !== "object") return out;
    for (const k of Object.keys(DEFAULTS)) if (typeof saved[k] === typeof DEFAULTS[k]) out[k] = saved[k];
    if (!LAYOUTS.includes(out.layout)) out.layout = DEFAULTS.layout;
    return out;
  }
  function save(storage, o) {
    try { storage.setItem(KEY, JSON.stringify(o)); } catch { /* blocked: not remembered */ }
  }

  const api = { DEFAULTS, load, save };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.MapOptions = api;
})(typeof window !== "undefined" ? window : globalThis);
