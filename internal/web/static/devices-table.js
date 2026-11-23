// Sorting and remembered choices (per browser) of the devices table.
(function (root) {
  const KEY = "netmon.devices.v1";
  const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });
  const defaults = () => ({ hidden: ["sources"], sort: null });

  // ipKey is a string that sorts like the address; IPv4 comes before IPv6.
  function ipKey(ip) {
    const hex = (n) => Number(n).toString(16).padStart(2, "0");
    if (!ip.includes(":")) return "4" + ip.split(".").map(hex).join("");
    ip = ip.split("%")[0].replace(/(\d+)\.(\d+)\.(\d+)\.(\d+)$/, (_, a, b, c, d) => hex(a) + hex(b) + ":" + hex(c) + hex(d));
    const [head, tail] = ip.split("::").map((s) => (s ? s.split(":") : []));
    const groups = tail ? [...head, ...Array(Math.max(0, 8 - head.length - tail.length)).fill("0"), ...tail] : head;
    return "6" + groups.map((g) => g.padStart(4, "0")).join("");
  }

  const compare = {
    text: collator.compare,
    num: (a, b) => a - b,
    ip: (a, b) => (a < b ? -1 : a > b ? 1 : 0),
  };

  // order returns the indexes of values in sorted order; empty values come last either way.
  function order(values, type, dir) {
    const keys = type === "ip" ? values.map((v) => v && ipKey(v)) : values;
    const sign = dir === "desc" ? -1 : 1;
    return values.map((_, i) => i).sort((a, b) =>
      (keys[a] === "") - (keys[b] === "") || (keys[a] === "" ? 0 : sign * compare[type](keys[a], keys[b])));
  }

  // nextSort is the sort after clicking a header; null is the default order.
  function nextSort(sort, col) {
    if (!sort || sort.col !== col) return { col, dir: "asc" };
    return sort.dir === "asc" ? { col, dir: "desc" } : null;
  }

  function load(storage) {
    const out = defaults();
    let saved;
    try { saved = JSON.parse(storage.getItem(KEY)); } catch { return out; } // blocked or unreadable
    if (!saved || typeof saved !== "object") return out;
    if (Array.isArray(saved.hidden)) out.hidden = saved.hidden.filter((c) => typeof c === "string");
    const s = saved.sort;
    if (s && typeof s.col === "string" && ["asc", "desc"].includes(s.dir)) out.sort = { col: s.col, dir: s.dir };
    return out;
  }
  function save(storage, o) {
    try { storage.setItem(KEY, JSON.stringify(o)); } catch { /* blocked: not remembered */ }
  }

  const api = { defaults, ipKey, order, nextSort, load, save };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.DevicesTable = api;
})(typeof window !== "undefined" ? window : globalThis);
