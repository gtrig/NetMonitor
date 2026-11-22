// Network map page: loads /api/graph into Cytoscape.
(() => {
  const css = getComputedStyle(document.documentElement);
  const v = (name) => css.getPropertyValue(name).trim();
  const netSel = document.getElementById("net");
  const panel = document.getElementById("panel");

  const cy = cytoscape({
    container: document.getElementById("cy"),
    wheelSensitivity: 0.3,
    style: [
      { selector: "node", style: {
        "label": "data(label)", "font-size": 10, "color": v("--text"), "text-valign": "bottom", "text-margin-y": 4,
        "background-color": v("--ok"), "width": 18, "height": 18, "border-width": 2, "border-color": v("--surface"),
        "text-background-color": v("--surface"), "text-background-opacity": 0.8, "text-background-padding": 2 } },
      { selector: "node[kind = 'device'][!known]", style: { "background-color": v("--warn") } },
      { selector: "node[status = 'offline']", style: { "background-color": v("--idle"), "opacity": 0.7 } },
      { selector: "node[kind = 'router']", style: {
        "shape": "round-rectangle", "width": 34, "height": 24, "background-color": v("--router"),
        "font-weight": "bold", "font-size": 12 } },
      { selector: "node[?router]", style: {
        "shape": "round-rectangle", "width": 30, "height": 22, "border-color": v("--router"), "border-width": 3, "font-weight": "bold" } },
      { selector: "node[kind = 'hop']", style: {
        "shape": "diamond", "width": 14, "height": 14, "background-color": v("--idle"), "font-size": 9, "color": v("--muted") } },
      { selector: "node[kind = 'net']", style: {
        "shape": "round-rectangle", "background-color": v("--accent"), "background-opacity": 0.05,
        "border-width": 1.5, "border-style": "dashed", "border-color": v("--accent"),
        "label": "data(label)", "text-valign": "top", "text-halign": "center", "text-margin-y": -4,
        "font-size": 13, "font-weight": "bold", "color": v("--accent"), "padding": 18,
        "text-background-opacity": 0 } },
      { selector: "edge[kind = 'member']", style: {
        "line-style": "dashed", "line-color": v("--router"), "width": 1.5, "target-arrow-shape": "none" } },
      { selector: "edge[source ^= 'hop:'], edge[target ^= 'hop:']", style: { "width": 2, "line-color": v("--router") } },
      { selector: "node[kind = 'network']", style: {
        "shape": "round-rectangle", "width": 30, "height": 20, "background-color": v("--muted") } },
      { selector: "edge", style: {
        "width": 1.2, "line-color": v("--border"), "curve-style": "bezier",
        "label": "data(label)", "font-size": 8, "color": v("--muted"), "text-rotation": "autorotate",
        "text-background-color": v("--surface"), "text-background-opacity": 1 } },
      { selector: "edge[source ^= 'router:'][target ^= 'router:']", style: { "width": 3, "line-color": v("--router") } },
      { selector: ":selected", style: { "border-color": v("--accent"), "border-width": 3 } },
    ],
  });

  const cose = () => cy.layout({ name: "cose", animate: false, nodeRepulsion: 9000, idealEdgeLength: 70,
    nodeDimensionsIncludeLabels: true, randomize: true, nestingFactor: 0.8, gravity: 0.6, numIter: 2500,
    componentSpacing: 80 }).run();
  const layout = () => (cy.nodes("[kind = 'net']").length ? boxLayout() : cose());

  // boxLayout lays out the all-networks view deterministically: the network that ties the others
  // together (the VPN) sits in the middle, each other network sits on its own side of it, routers
  // inside the middle box face the network they serve, and each box holds its routers in the
  // centre with hosts in rings around them.
  function boxLayout() {
    const isRouter = (n) => n.data("router") || n.data("kind") === "router";
    const boxOf = (n) => (n.data("kind") === "net" ? n.id() : n.parent().length ? n.parent().id() : null);
    const boxes = cy.nodes("[kind = 'net']").map((b) => b.id());
    const tie = {};
    cy.edges().forEach((e) => {
      const a = boxOf(e.source()), b = boxOf(e.target());
      if (!a || !b || a === b) return;
      (tie[a] ??= {})[b] = (tie[a][b] || 0) + 1;
      (tie[b] ??= {})[a] = (tie[b][a] || 0) + 1;
    });
    const score = (id) => [Object.keys(tie[id] || {}).length, Object.values(tie[id] || {}).reduce((x, y) => x + y, 0)];
    const hub = boxes.slice().sort((a, b) => {
      const [x1, y1] = score(a), [x2, y2] = score(b);
      return x2 - x1 || y2 - y1 || a.localeCompare(b);
    })[0];
    const outer = boxes.filter((id) => id !== hub);
    const angle = {};
    outer.forEach((id, i) => { angle[id] = Math.PI + (2 * Math.PI * i) / outer.length; });

    const place = (n, x, y) => n.position({ x, y });
    // Routers in the centre, everything else in rings around them, centred on (0, 0).
    const rings = (nodes) => {
      const sorted = nodes.sort((a, b) => String(a.data("label")).localeCompare(String(b.data("label"))));
      const routers = sorted.filter(isRouter), rest = sorted.filter((n) => !isRouter(n));
      routers.forEach((n, i) => place(n, (i - (routers.length - 1) / 2) * 70, 0));
      const base = routers.length > 1 ? routers.length * 35 : 0;
      for (let i = 0, k = 1; i < rest.length; k++) {
        const r = base + k * 75, m = Math.min(Math.max(1, Math.floor((2 * Math.PI * r) / 85)), rest.length - i);
        for (let j = 0; j < m; j++) {
          const a = (2 * Math.PI * j) / m - Math.PI / 2;
          place(rest[i + j], r * Math.cos(a), r * Math.sin(a));
        }
        i += m;
      }
    };
    const shift = (nodes, dx, dy) => nodes.forEach((n) => place(n, n.position("x") + dx, n.position("y") + dy));

    // Middle box: routers facing an outer network go on that network's side.
    const hubKids = cy.getElementById(hub).children();
    const side = {}, middle = [];
    hubKids.forEach((n) => {
      const c = {};
      n.connectedEdges().forEach((e) => {
        const o = e.source().same(n) ? e.target() : e.source();
        const b = boxOf(o);
        if (b && b !== hub) c[b] = (c[b] || 0) + 1;
      });
      const best = Object.keys(c).sort((a, b) => c[b] - c[a] || a.localeCompare(b))[0];
      best ? (side[best] ??= []).push(n) : middle.push(n);
    });
    rings(cy.collection(middle));
    const r0 = (middle.length > 1 ? (75 * middle.length) / Math.PI : 0) + 160;
    for (const [b, list] of Object.entries(side)) {
      const a = angle[b] ?? 0, px = -Math.sin(a), py = Math.cos(a);
      list.forEach((n, j) => {
        const off = (j - (list.length - 1) / 2) * 70;
        place(n, r0 * Math.cos(a) + px * off, r0 * Math.sin(a) + py * off);
      });
    }

    const size = (nodes) => {
      const bb = nodes.boundingBox({ includeLabels: true });
      return nodes.length ? { w: bb.w + 40, h: bb.h + 40, cx: (bb.x1 + bb.x2) / 2, cy: (bb.y1 + bb.y2) / 2 } : { w: 80, h: 60, cx: 0, cy: 0 };
    };
    const hs = size(hubKids);
    const gap = 140;
    for (const id of outer) {
      const kids = cy.getElementById(id).children();
      rings(kids);
      const os = size(kids), a = angle[id], c = Math.cos(a), s = Math.sin(a);
      const d = Math.min(Math.abs(c) > 1e-6 ? ((hs.w + os.w) / 2 + gap) / Math.abs(c) : Infinity,
        Math.abs(s) > 1e-6 ? ((hs.h + os.h) / 2 + gap) / Math.abs(s) : Infinity);
      shift(kids, hs.cx + d * c - os.cx, hs.cy + d * s - os.cy);
    }
    // Anything outside the boxes (hops beyond the monitored networks) goes in a row underneath.
    const loose = cy.nodes().filter((n) => n.data("kind") !== "net" && !n.parent().length);
    if (loose.length) {
      const bb = cy.nodes("[kind = 'net']").boundingBox();
      loose.forEach((n, i) => place(n, bb.x1 + i * 90, bb.y2 + 80));
    }
    cy.fit(undefined, 30);
  }

  async function load() {
    const q = netSel.value ? "?network=" + encodeURIComponent(netSel.value) : "";
    try {
      const els = await api("GET", "/api/graph" + q);
      cy.elements().remove();
      cy.add(els);
      layout();
      if (!els.length) toast("No devices yet — waiting for the first scan");
    } catch (err) { toast("Could not load graph: " + err.message); }
  }

  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const ago = (t) => { if (!t || t.startsWith("0001")) return "never"; const d = (Date.now() - new Date(t)) / 1000;
    return d < 60 ? Math.round(d) + "s ago" : d < 3600 ? Math.round(d / 60) + "m ago" : d < 172800 ? Math.round(d / 3600) + "h ago" : new Date(t).toLocaleDateString(); };

  function show(n) {
    const d = n.data();
    if (d.kind === "net") {
      const kids = n.children();
      const on = kids.filter((c) => c.data("status") === "online").length;
      panel.innerHTML = `<button class="btn small close">×</button><h2>${esc(d.label)}</h2>
        <p class="muted">${kids.length} nodes · ${on} online</p>
        <p><a href="/map?network=${encodeURIComponent(d.network)}">Only this network →</a></p>`;
    } else if (d.kind !== "device") {
      panel.innerHTML = `<button class="btn small close">×</button><h2>${esc(d.label)}</h2><p class="muted">${esc(d.kind === "hop" ? "router hop (from traceroute)" : d.kind)}${d.network ? " in " + esc(d.network) : ""} · ${n.degree()} links</p>`;
    } else {
      const members = d.members || [];
      const memberList = members.length > 1 ? `<h3 class="sub">Addresses</h3><table class="members"><tbody>${members.map((m) =>
        `<tr><td>${esc(m.network)}</td><td class="mono">${esc(m.ip)}</td><td class="mono small">${esc(m.mac)}</td><td><span class="pill ${m.online ? "ok" : "idle"}"></span></td></tr>`).join("")}</tbody></table>` : "";
      panel.innerHTML = `<button class="btn small close">×</button>
        <h2>${esc(d.label)}</h2>
        <dl><dt>Status</dt><dd>${esc(d.status)}</dd><dt>IP</dt><dd class="mono">${esc(d.ip)}</dd>
        <dt>MAC</dt><dd class="mono">${esc(d.mac)}</dd><dt>Vendor</dt><dd>${esc(d.vendor)}</dd>
        <dt>Hostname</dt><dd>${esc(d.hostname)}</dd><dt>Port</dt><dd class="mono">${esc(d.port)}</dd>
        <dt>Network</dt><dd>${esc(d.network)}</dd><dt>Last seen</dt><dd>${esc(ago(d.lastSeen))}</dd>
        <dt>First seen</dt><dd>${esc(ago(d.firstSeen))}</dd></dl>${memberList}
        <div class="details-box"><span class="muted small">Loading services…</span></div>
        <form><input class="label" name="label" placeholder="Label" value="${esc(n.data("userLabel") || "")}" aria-label="Label">
        <label class="check"><input type="checkbox" name="known" ${d.known ? "checked" : ""}> known device</label>
        <button class="btn">Save</button></form>`;
      panel.querySelector("form").addEventListener("submit", async (e) => {
        e.preventDefault();
        const f = e.target;
        try {
          const fields = { known: f.known.checked };
          if (f.label.value.trim()) fields.label = f.label.value.trim();
          const keys = [...new Set((d.members && d.members.length ? d.members : [{ key: d.key }]).map((m) => m.key))];
          for (const k of keys) await patchDevice(k, fields);
          n.data("known", f.known.checked);
          if (fields.label) n.data("label", fields.label);
          toast("Saved");
        } catch (err) { toast("Save failed: " + err.message); }
      });
    }
    if (d.kind === "device") loadDetails(d);
    panel.querySelector(".close").addEventListener("click", () => { panel.hidden = true; cy.elements().unselect(); });
    panel.hidden = false;
  }

  // Detailed-scan results (open ports, services, OS) of every address of a device or machine.
  async function loadDetails(d) {
    const box = panel.querySelector(".details-box");
    const members = d.members && d.members.length ? d.members : [{ network: d.network, key: d.key, ip: d.ip }];
    let all = [];
    try { all = await api("GET", "/api/details"); } catch { /* shown as none */ }
    if (!box.isConnected) return;
    const byRef = new Map(all.map((x) => [x.network + "|" + x.key, x]));
    box.innerHTML = `<h3 class="sub">Services</h3>` + members.map((m) => {
      const x = byRef.get(m.network + "|" + m.key);
      const head = members.length > 1 ? `<div class="small mono">${esc(m.ip)} <span class="muted">${esc(m.network)}</span></div>` : "";
      const body = !x ? `<div class="muted small">not scanned yet</div>`
        : (x.os ? `<div class="small">OS: ${esc(x.os)} <span class="muted">(${x.osAccuracy}%)</span></div>` : "") +
          (x.ports.length ? `<table class="ports"><tbody>${x.ports.map((p) => `<tr><td class="mono">${p.port}/${esc(p.proto)}</td><td>${esc(p.service)}</td><td class="small">${esc(p.product)} ${esc(p.version)}</td></tr>`).join("")}</tbody></table>`
            : `<div class="muted small">no open ports</div>`) +
          `<div class="muted small">scanned ${esc(ago(x.scannedAt))}</div>`;
      return `<div class="member-details">${head}${body}
        <button class="btn small" type="button" data-scan-net="${esc(m.network)}" data-scan-key="${esc(m.key)}">${x ? "Rescan" : "Scan details now"}</button></div>`;
    }).join("");
    box.addEventListener("click", async (e) => {
      const b = e.target.closest("[data-scan-key]");
      if (!b) return;
      try {
        await api("POST", "/api/details/scan", { network: b.dataset.scanNet, key: b.dataset.scanKey });
        b.disabled = true; b.textContent = "Scanning…";
        toast("Detailed scan started; progress is on the Overview page");
      } catch (err) { toast("Scan failed: " + err.message); }
    });
  }

  cy.on("tap", "node", (e) => show(e.target));
  cy.on("tap", (e) => { if (e.target === cy) panel.hidden = true; });
  netSel.addEventListener("change", () => {
    const u = new URL(location); netSel.value ? u.searchParams.set("network", netSel.value) : u.searchParams.delete("network");
    history.replaceState(null, "", u); load();
  });
  document.getElementById("relayout").addEventListener("click", layout);
  load();
  setInterval(async () => { // refresh statuses without re-layout
    try {
      const q = netSel.value ? "?network=" + encodeURIComponent(netSel.value) : "";
      const els = await api("GET", "/api/graph" + q);
      const ids = new Set(els.map((e) => e.data.id));
      const missing = els.some((e) => !cy.getElementById(e.data.id).length) || cy.elements().some((e) => !ids.has(e.id()));
      if (missing) { cy.elements().remove(); cy.add(els); layout(); return; }
      for (const e of els) cy.getElementById(e.data.id).data(e.data);
    } catch { /* ignore transient errors */ }
  }, 60000);
})();
