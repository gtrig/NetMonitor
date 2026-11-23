// Map layouts: pure functions that turn graph elements into positions and background shapes.
(function (root) {
  const MIN = 0.1, MAX = 300, TICKS = [0.1, 1, 10, 100];
  const R0 = 55, OUTER_GAP = 34, LABEL_PAD = 70, GROUP_GAP = 70;
  const LANE = { left: 120, width: 900, strip: 80, row: 36, pad: 22, minGap: 74 };
  const GOLDEN = Math.PI * (3 - Math.sqrt(5));

  // frac places ms on a log scale from 0 (0.1 ms or less) to 1 (300 ms or more).
  const frac = (ms) => Math.min(1, Math.max(0, Math.log(Math.max(ms, MIN) / MIN) / Math.log(MAX / MIN)));

  const nodesOf = (els) => els.filter((e) => e.group === "nodes").map((e) => e.data);
  const byText = (a, b) => a.localeCompare(b, undefined, { numeric: true });

  function networksOf(nodes) {
    const boxes = nodes.filter((d) => d.kind === "net").map((d) => d.network);
    if (boxes.length) return boxes;
    return [...new Set(nodes.flatMap((d) => d.networks || []))];
  }

  // prepare replaces the server's stand-in network nodes by "netmon:<network>" markers and adds
  // one to every network whose times are measured by netmon itself.
  function prepare(els) {
    const fix = (id) => (typeof id === "string" && id.startsWith("network:") ? "netmon:" + id.slice(8) : id);
    const out = els.map((e) => {
      const d = { ...e.data };
      if (e.group === "edges") { d.source = fix(d.source); d.target = fix(d.target); }
      else if (d.kind === "network") { d.network = d.label; d.id = fix(d.id); d.kind = "netmon"; d.label = "netmon"; }
      return { ...e, data: d };
    });
    const have = new Map(nodesOf(out).map((d) => [d.id, d]));
    for (const g of groups(out)) {
      if (!g.centre.startsWith("netmon:") || have.has(g.centre)) continue;
      const data = { id: g.centre, kind: "netmon", label: "netmon", network: g.network };
      if (have.has("net:" + g.network)) data.parent = "net:" + g.network;
      out.push({ group: "nodes", data });
    }
    return out;
  }

  // groups splits the nodes by network, each around its router or a netmon marker.
  function groups(els) {
    const nodes = nodesOf(els).filter((d) => d.kind !== "net");
    const list = networksOf(nodesOf(els)).map((network) => ({ network, label: network, centre: "", nodes: [] }));
    const by = new Map(list.map((g) => [g.network, g]));
    const placed = new Set();
    for (const d of nodes) {
      const n = (d.gatewayOf || []).find((x) => by.has(x) && !by.get(x).centre);
      if (n) { by.get(n).centre = d.id; placed.add(d.id); }
    }
    for (const g of list) if (!g.centre) { g.centre = "netmon:" + g.network; placed.add(g.centre); }
    for (const d of nodes) {
      if (placed.has(d.id)) continue;
      const parent = typeof d.parent === "string" && d.parent.startsWith("net:") ? d.parent.slice(4) : "";
      const g = by.get(parent) || by.get((d.networks || [])[0]) || by.get(d.network);
      if (g) g.nodes.push(d);
    }
    for (const g of list) g.nodes.sort((a, b) => byText(a.id, b.id));
    return list;
  }

  const msOf = (d, network) => (d.status === "offline" ? null : d.rtt?.[network]?.ms ?? null);
  const portOf = (d, network) => (d.members || []).find((m) => m.network === network && m.port)?.port || "no port";

  function ports(g, nodes) {
    const m = new Map();
    for (const d of nodes) { const p = portOf(d, g.network); m.has(p) ? m.get(p).push(d) : m.set(p, [d]); }
    return [...m].sort(([a], [b]) => (a === "no port") - (b === "no port") || byText(a, b));
  }

  // Each drawer lays a group out around (0, 0) and returns its half extents.
  function circle(mode, g, nodes, o) {
    const r1 = Math.max(190, 120 + nodes.length * 8), outer = r1 + OUTER_GAP;
    const pos = { [g.centre]: { x: 0, y: 0 } }, decor = [];
    const radius = (d) => {
      if (!o.distance) return r1;
      const ms = msOf(d, g.network);
      return ms == null ? outer : R0 + frac(ms) * (r1 - R0);
    };
    const put = (d, a) => { const r = radius(d); pos[d.id] = { x: r * Math.cos(a), y: r * Math.sin(a) }; };
    if (mode === "sectors") {
      const ps = ports(g, nodes), full = 2 * Math.PI, min = Math.min(0.35, full / Math.max(1, ps.length));
      const raw = ps.map(([, ds]) => Math.max(min, (full * ds.length) / nodes.length));
      // Shrink only what is above the minimum, so small wedges keep it.
      const total = raw.reduce((a, b) => a + b, 0), spare = raw.reduce((a, b) => a + (b - min), 0);
      const k = spare > 0 ? Math.max(0, (spare - (total - full)) / spare) : 1;
      let a = -Math.PI / 2;
      ps.forEach(([label, ds], i) => {
        const w = min + (raw[i] - min) * k;
        const pad = Math.min(0.03, w / 10);
        decor.push({ network: g.network, kind: "wedge", x: 0, y: 0, r: outer + 14, a0: a + pad, a1: a + w - pad, label });
        ds.forEach((d, j) => put(d, a + (w * (j + 0.5)) / ds.length));
        a += w;
      });
    } else {
      nodes.forEach((d, i) => put(d, -Math.PI / 2 + i * GOLDEN));
    }
    if (o.distance) {
      for (const t of TICKS) decor.push({ network: g.network, kind: "ring", x: 0, y: 0, r: R0 + frac(t) * (r1 - R0), label: t + " ms" });
      decor.push({ network: g.network, kind: "outer", x: 0, y: 0, r: outer, label: "no reply" });
    }
    const h = outer + LABEL_PAD;
    return { pos, decor, x0: -h, x1: h, y0: -h, y1: h };
  }

  function lanes(g, nodes, o) {
    const pos = {}, decor = [], right = LANE.left + LANE.width, strip = right + LANE.strip;
    const index = new Map(nodes.map((d, i) => [d.id, i]));
    const xOf = (d) => {
      if (!o.distance) return LANE.left + 40 + ((right - LANE.left - 40) * index.get(d.id)) / Math.max(1, nodes.length - 1);
      const ms = msOf(d, g.network);
      return ms == null ? strip : LANE.left + 40 + frac(ms) * (right - LANE.left - 40);
    };
    let y = 0;
    for (const [label, ds] of ports(g, nodes)) {
      const last = []; // x of the last device in each row
      const row = new Map();
      for (const d of ds.slice().sort((a, b) => xOf(a) - xOf(b) || byText(a.id, b.id))) {
        let r = last.findIndex((x) => xOf(d) - x >= LANE.minGap);
        if (r < 0) r = last.length;
        last[r] = xOf(d); row.set(d.id, r);
      }
      const h = last.length * LANE.row + LANE.pad;
      for (const d of ds) pos[d.id] = { x: xOf(d), y: y + LANE.pad / 2 + 10 + row.get(d.id) * LANE.row };
      decor.push({ network: g.network, kind: "lane", x: LANE.left, y, w: strip + 60 - LANE.left, h, label });
      y += h + 6;
    }
    const height = Math.max(y, 60);
    pos[g.centre] = { x: 0, y: height / 2 };
    if (o.distance && nodes.length) {
      for (const t of TICKS) decor.push({ network: g.network, kind: "tick", x: LANE.left + 40 + frac(t) * (right - LANE.left - 40), y0: 0, y1: height, label: t + " ms" });
      decor.push({ network: g.network, kind: "tick", x: strip, y0: 0, y1: height, label: "no reply", outer: true });
    }
    return { pos, decor, x0: -70, x1: strip + 70, y0: -30, y1: height + 30 };
  }

  // arrange returns an offset per group: the best connected one in the middle, the rest around
  // it, or all stacked.
  function arrange(mode, els, gs, drawn) {
    if (mode === "lanes") {
      let y = 0;
      return gs.map((_, i) => { const at = { x: 0, y: y - drawn[i].y0 }; y += drawn[i].y1 - drawn[i].y0; return at; });
    }
    const of = new Map();
    gs.forEach((g, i) => { of.set("net:" + g.network, i); of.set(g.centre, i); g.nodes.forEach((d) => of.set(d.id, i)); });
    const tie = gs.map(() => new Map());
    for (const e of els) {
      if (e.group !== "edges") continue;
      const a = of.get(e.data.source), b = of.get(e.data.target);
      if (a === undefined || b === undefined || a === b) continue;
      tie[a].set(b, (tie[a].get(b) || 0) + 1); tie[b].set(a, (tie[b].get(a) || 0) + 1);
    }
    const sum = (m) => [...m.values()].reduce((x, y) => x + y, 0);
    const order = gs.map((_, i) => i).sort((i, j) => tie[j].size - tie[i].size || sum(tie[j]) - sum(tie[i]) || byText(gs[i].network, gs[j].network));
    const hub = order[0], rest = gs.map((_, i) => i).filter((i) => i !== hub), k = rest.length;
    const at = gs.map(() => ({ x: 0, y: 0 }));
    // Beyond two, groups also sit diagonally, where square boxes need more room than circles.
    const size = (i) => drawn[i].x1 * (k > 2 ? Math.SQRT2 : 1);
    const biggest = Math.max(0, ...rest.map(size));
    const apart = k > 2 ? (biggest + GROUP_GAP / 2) / Math.sin(Math.PI / k) : 0;
    rest.forEach((i, j) => {
      const a = Math.PI + (2 * Math.PI * j) / k;
      const r = k > 2 ? Math.max(size(hub) + biggest + GROUP_GAP, apart) : size(hub) + size(i) + GROUP_GAP;
      at[i] = { x: r * Math.cos(a), y: r * Math.sin(a) };
    });
    return at;
  }

  function layout(mode, els, o) {
    const all = groups(els);
    const visible = (d) => o.offline || d.status !== "offline";
    const gs = all;
    const drawn = gs.map((g) => { const ns = g.nodes.filter(visible); return mode === "lanes" ? lanes(g, ns, o) : circle(mode, g, ns, o); });
    const at = arrange(mode, els, gs, drawn);
    const positions = {}, decor = [];
    let bottom = 0, left = 0;
    drawn.forEach((d, i) => {
      const { x, y } = at[i];
      for (const [id, p] of Object.entries(d.pos)) positions[id] = { x: p.x + x, y: p.y + y };
      for (const s of d.decor) decor.push("y0" in s ? { ...s, x: s.x + x, y0: s.y0 + y, y1: s.y1 + y } : { ...s, x: s.x + x, y: s.y + y });
      bottom = Math.max(bottom, d.y1 + y); left = Math.min(left, d.x0 + x);
    });
    // Nodes outside every network (hops on the way) go in a row underneath.
    nodesOf(els).filter((d) => d.kind !== "net" && !positions[d.id] && visible(d))
      .forEach((d, i) => { positions[d.id] = { x: left + 60 + i * 90, y: bottom + 50 }; });
    return { positions, decor };
  }

  const api = { frac, prepare, groups, layout };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.MapLayout = api;
})(typeof window !== "undefined" ? window : globalThis);
