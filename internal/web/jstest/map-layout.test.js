const test = require("node:test");
const assert = require("node:assert");
const L = require("../static/map-layout.js");

const node = (data) => ({ group: "nodes", data });
const edge = (source, target) => ({ group: "edges", data: { id: "e:" + source + target, source, target, kind: "link" } });
const net = (n) => node({ id: "net:" + n, kind: "net", network: n, label: n });
const dev = (id, ms, x = {}) => node({ id, kind: "device", label: id, status: "online", networks: ["lan"], network: "lan", parent: "net:lan",
  members: [{ network: "lan", port: x.port || "" }], ...(ms == null ? {} : { rtt: { lan: { ms } } }), ...x });
const gw = (id, n, x = {}) => node({ id, kind: "device", label: id, status: "online", networks: [n], network: n, parent: "net:" + n, gatewayOf: [n], router: true, members: [], ...x });
const lan = (...devs) => [net("lan"), gw("gw", "lan"), ...devs, ...devs.map((d) => edge("gw", d.data.id))];
const run = (mode, els, o = {}) => L.layout(mode, els, { distance: true, offline: true, ...o });
const modes = ["radar", "sectors", "lanes"];
const dist = (mode, p, c) => (mode === "lanes" ? p.x - c.x : Math.hypot(p.x - c.x, p.y - c.y));
const near = (a, b) => Math.abs(a - b) < 1e-6;

test("frac is logarithmic and clamped", () => {
  assert.equal(L.frac(0.1), 0); assert.equal(L.frac(300), 1); assert.equal(L.frac(0), 0); assert.equal(L.frac(5000), 1);
  assert.ok(Math.abs(L.frac(10) - 0.575) < 0.001);
});

for (const mode of modes) {
  test(mode + ": more ms is never nearer", () => {
    const ms = [0, 0.1, 0.5, 2, 10, 80, 200, 300, 900];
    const { positions: p } = run(mode, lan(...ms.map((m, i) => dev("d" + i, m, { port: "e" + (i % 3) }))));
    const ds = ms.map((_, i) => dist(mode, p["d" + i], p.gw));
    for (let i = 1; i < ds.length; i++) assert.ok(ds[i] >= ds[i - 1] - 1e-6, `${ds}`);
    assert.ok(ds[8] > ds[0] + 50);
  });
  test(mode + ": offline and unanswered sit at the same outer distance", () => {
    const { positions: p } = run(mode, lan(dev("far", 900), dev("off", 5, { status: "offline" }), dev("mute", null)));
    assert.ok(near(dist(mode, p.off, p.gw), dist(mode, p.mute, p.gw)));
    assert.ok(dist(mode, p.off, p.gw) > dist(mode, p.far, p.gw));
  });
  test(mode + ": offline devices can be left out", () => {
    const { positions: p } = run(mode, lan(dev("a", 1), dev("off", null, { status: "offline" })), { offline: false });
    assert.ok(p.a && !p.off);
  });
  test(mode + ": finite positions for odd graphs", () => {
    const graphs = [[], [net("lan")], lan(dev("a", 1)), [net("lan"), dev("a", null)], [net("lan"), dev("a", null), dev("b", 3)],
      [dev("a", null, { parent: undefined })], [node({ id: "hop:1.1.1.1", kind: "hop", label: "x", network: "" })],
      [node({ id: "router:x", kind: "router", label: "x" }), dev("a", 1)]];
    for (const g of graphs) {
      const els = L.prepare(g);
      const { positions: p, decor } = run(mode, els);
      for (const e of els) if (e.group === "nodes" && e.data.kind !== "net") assert.ok(p[e.data.id], e.data.id + " unplaced");
      for (const q of Object.values(p)) assert.ok(Number.isFinite(q.x) && Number.isFinite(q.y));
      for (const d of decor) for (const v of Object.values(d)) if (typeof v === "number") assert.ok(Number.isFinite(v));
    }
  });
  test(mode + ": groups do not overlap", () => {
    for (const k of [2, 3, 4, 6]) {
      const els = [];
      for (let g = 0; g < k; g++) {
        const n = "n" + g;
        els.push(net(n), gw("gw" + g, n));
        for (let i = 0; i < 5 + g * 8; i++) els.push(dev(`${n}-${i}`, i + 1, { networks: [n], parent: "net:" + n, members: [{ network: n, port: "e" + (i % 4) }], rtt: { [n]: { ms: i + 1 } } }));
        if (g) els.push(edge("gw0", "gw" + g));
      }
      const { positions: p } = run(mode, els);
      const boxes = [];
      for (let g = 0; g < k; g++) {
        const q = Object.keys(p).filter((id) => id === "gw" + g || id.startsWith(`n${g}-`)).map((id) => p[id]);
        boxes.push([Math.min(...q.map((a) => a.x)), Math.max(...q.map((a) => a.x)), Math.min(...q.map((a) => a.y)), Math.max(...q.map((a) => a.y))]);
      }
      for (let i = 0; i < k; i++) for (let j = 0; j < i; j++) {
        const [a, b] = [boxes[i], boxes[j]];
        assert.ok(a[1] < b[0] || b[1] < a[0] || a[3] < b[2] || b[3] < a[2], `k=${k} ${i}/${j} ${a} ${b}`);
      }
    }
  });
}

test("radar: a device keeps its direction when its time changes", () => {
  const ang = (ms) => { const { positions: p } = run("radar", lan(dev("a", 3), dev("b", ms), dev("c", 8))); return Math.atan2(p.b.y - p.gw.y, p.b.x - p.gw.x); };
  assert.ok(near(ang(1), ang(200)));
});

test("distance off: equal radii and no scale", () => {
  for (const mode of ["radar", "sectors"]) {
    const { positions: p, decor } = run(mode, lan(dev("a", 1), dev("b", 200), dev("c", null)), { distance: false });
    assert.ok(near(dist(mode, p.a, p.gw), dist(mode, p.b, p.gw)) && near(dist(mode, p.a, p.gw), dist(mode, p.c, p.gw)));
    assert.ok(!decor.some((d) => d.kind === "ring"));
  }
  const { positions: p, decor } = run("lanes", lan(dev("a", 1), dev("b", 200), dev("c", 300)), { distance: false });
  assert.ok(near(p.b.x - p.a.x, p.c.x - p.b.x));
  assert.ok(!decor.some((d) => d.kind === "tick"));
});

test("sectors: wedges fit without overlapping for 12 ports", () => {
  const devs = [];
  for (let i = 0; i < 40; i++) devs.push(dev("d" + i, i, { port: i < 20 ? "wifi" : "e" + (i % 11) }));
  const w = run("sectors", lan(...devs)).decor.filter((d) => d.kind === "wedge");
  assert.equal(w.length, 12);
  for (let i = 1; i < w.length; i++) assert.ok(w[i].a0 >= w[i - 1].a1 - 1e-9);
  assert.ok(w.every((x) => x.a1 > x.a0) && w.at(-1).a1 - w[0].a0 <= 2 * Math.PI + 1e-9);
});

test("a device without a port is grouped under no port", () => {
  for (const [mode, kind] of [["sectors", "wedge"], ["lanes", "lane"]])
    assert.deepEqual(run(mode, lan(dev("a", 1, { port: "ether2" }), dev("b", 1))).decor.filter((d) => d.kind === kind).map((d) => d.label), ["ether2", "no port"]);
});

test("a LAN's gateway centres it even when its parent is the VPN", () => {
  const els = L.prepare([net("lan"), net("vpn"), gw("gw", "lan", { networks: ["lan", "vpn"], parent: "net:vpn" }), dev("a", 1), dev("v", 1, { networks: ["vpn"], parent: "net:vpn" })]);
  const g = L.groups(els);
  assert.equal(g.find((x) => x.network === "lan").centre, "gw");
  assert.equal(g.find((x) => x.network === "vpn").centre, "netmon:vpn");
  assert.ok(els.some((e) => e.data.id === "netmon:vpn"));
});

test("a gateway of two networks centres the first; the other gets a marker", () => {
  const g = L.groups(L.prepare([net("a"), net("b"), gw("gw", "a", { gatewayOf: ["a", "b"], networks: ["a", "b"] })]));
  assert.deepEqual(g.map((x) => x.centre), ["gw", "netmon:b"]);
});

test("prepare turns a network stand-in node into the netmon marker", () => {
  const els = L.prepare([node({ id: "network:lan", kind: "network", label: "lan" }), dev("a", 1, { parent: undefined }), edge("network:lan", "a")]);
  assert.equal(els.filter((e) => e.group === "nodes" && e.data.id === "netmon:lan").length, 1);
  assert.ok(els.some((e) => e.data.source === "netmon:lan" && e.data.target === "a"));
  assert.ok(!els.some((e) => e.data.id === "network:lan"));
});
