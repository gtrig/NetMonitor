// Network map page: loads /api/graph into Cytoscape. Positions come from map-layout.js; the scale
// is drawn on a canvas under the graph and the names on one above it.
(() => {
  const css = getComputedStyle(document.documentElement);
  const v = (name) => css.getPropertyValue(name).trim();
  const netSel = document.getElementById("net");
  const layoutSel = document.getElementById("layout");
  const panel = document.getElementById("panel");
  const notice = document.getElementById("notice");
  let storage; // reading the property itself can throw
  try { storage = window.localStorage; } catch { /* blocked */ }
  const opts = MapOptions.load(storage);

  const graphStyle = () => [
    { selector: "node", style: { "background-color": v("--ok"), "width": 16, "height": 16, "border-width": 2, "border-color": v("--surface") } },
    { selector: "node[kind = 'device'][!known]", style: { "background-color": v("--warn") } },
    { selector: "node[status = 'offline']", style: { "background-color": v("--surface"), "border-color": v("--idle"), "width": 13, "height": 13 } },
    { selector: "node[kind = 'router'], node[?router]", style: { "shape": "round-rectangle", "width": 40, "height": 26, "background-color": v("--router") } },
    { selector: "node[?router][status = 'offline']", style: { "background-color": v("--surface"), "border-color": v("--router") } },
    { selector: "node[kind = 'netmon']", style: { "shape": "round-rectangle", "width": 34, "height": 22, "background-color": v("--muted") } },
    { selector: "node[kind = 'hop']", style: { "shape": "diamond", "width": 13, "height": 13, "background-color": v("--idle") } },
    { selector: "node[kind = 'net']", style: { "shape": "round-rectangle", "background-opacity": 0, "border-width": 0, "padding": 24, "width": 40, "height": 40 } },
    { selector: "edge", style: { "width": 1, "line-color": v("--idle"), "line-opacity": 0.4, "curve-style": "straight" } },
    { selector: "edge[kind = 'member']", style: { "line-style": "dashed", "line-color": v("--router"), "line-opacity": 1, "width": 1.5 } },
    { selector: "edge.trunk", style: { "width": 2.5, "line-color": v("--router"), "line-opacity": 0.8 } },
    { selector: "node[kind != 'net']:selected", style: { "border-color": v("--accent"), "border-width": 3 } },
    { selector: ".off", style: { "display": "none" } },
  ];
  const cy = cytoscape({ container: document.getElementById("cy"), wheelSensitivity: 0.3, minZoom: 0.1, maxZoom: 5,
    autoungrabify: true, style: graphStyle() });

  let els = [], decor = [], hover = null;
  const trunkEnd = (n) => n.data("router") || ["router", "hop", "netmon"].includes(n.data("kind"));

  // apply puts the elements and their positions into the graph. Existing nodes glide to their new
  // place when animated; pan, zoom and selection stay unless refit.
  function apply({ animate = false, refit = false } = {}) {
    const ids = new Set(els.map((e) => e.data.id));
    const { positions, decor: shapes } = MapLayout.layout(opts.layout, els, opts);
    decor = shapes;
    cy.batch(() => {
      cy.elements().filter((e) => !ids.has(e.id())).remove();
      for (const group of ["nodes", "edges"]) {
        // Boxes first: a child can only be added to or moved into an existing parent.
        const list = els.filter((e) => e.group === group).sort((a, b) => (b.data.kind === "net") - (a.data.kind === "net"));
        for (const e of list) {
          const { id, parent, source, target, ...data } = e.data;
          let cur = cy.getElementById(id);
          if (group === "edges" && cur.length && (cur.data("source") !== source || cur.data("target") !== target)) { cur.remove(); cur = cy.getElementById(id); }
          if (!cur.length) { cy.add({ group, data: { ...e.data }, position: positions[id] && { ...positions[id] } }); continue; }
          cur.data(data);
          for (const k of ["rtt", "gatewayOf", "router", "machine"]) if (!(k in data)) cur.removeData(k);
          if (group === "nodes" && (cur.data("parent") || undefined) !== parent) cur.move({ parent: parent ?? null });
        }
      }
      cy.edges().forEach((e) => {
        e.toggleClass("trunk", e.data("kind") !== "member" && !!trunkEnd(e.source()) && !!trunkEnd(e.target()));
        e.toggleClass("off", !opts.lines);
      });
      cy.nodes("[kind != 'net']").forEach((n) => { n.toggleClass("off", !positions[n.id()]); });
    });
    const still = !animate || matchMedia("(prefers-reduced-motion: reduce)").matches;
    cy.nodes("[kind != 'net']").forEach((n) => {
      const p = positions[n.id()];
      if (!p) return;
      n.stop(true);
      const q = n.position();
      if (Math.abs(p.x - q.x) < 0.5 && Math.abs(p.y - q.y) < 0.5) return;
      still ? n.position({ ...p }) : n.animate({ position: { ...p } }, { duration: 400 });
    });
    if (refit) fit(positions);
    cy.forceRender();
  }

  function fit(positions) {
    const xs = [], ys = [];
    for (const p of Object.values(positions)) { xs.push(p.x - 50, p.x + 50); ys.push(p.y - 40, p.y + 50); }
    for (const d of decor) {
      if (d.kind === "outer" || d.kind === "wedge") { xs.push(d.x - d.r, d.x + d.r); ys.push(d.y - d.r - 34, d.y + d.r); }
      else if (d.kind === "lane") { xs.push(d.x, d.x + d.w); ys.push(d.y - 34, d.y + d.h + 20); }
    }
    if (opts.ports && opts.layout === "sectors") { xs.push(Math.min(...xs) - 40, Math.max(...xs) + 40); ys.push(Math.max(...ys) + 30); }
    if (!xs.length) return;
    const x0 = Math.min(...xs), x1 = Math.max(...xs), y0 = Math.min(...ys), y1 = Math.max(...ys);
    const zoom = Math.max(cy.minZoom(), Math.min(1.6, cy.width() / (x1 - x0 + 30), cy.height() / (y1 - y0 + 30)));
    cy.viewport({ zoom, pan: { x: (cy.width() - (x0 + x1) * zoom) / 2, y: (cy.height() - (y0 + y1) * zoom) / 2 } });
  }

  function surface(id) {
    const c = document.getElementById(id), r = window.devicePixelRatio || 1, w = c.clientWidth, h = c.clientHeight;
    if (c.width !== Math.round(w * r) || c.height !== Math.round(h * r)) { c.width = Math.round(w * r); c.height = Math.round(h * r); }
    const g = c.getContext("2d");
    g.setTransform(r, 0, 0, r, 0, 0);
    g.clearRect(0, 0, w, h);
    return g;
  }
  const fmtMs = (ms) => (ms < 10 ? ms.toFixed(1) : String(Math.round(ms))) + " ms";

  function drawDecor(g, z, sx, sy) {
    for (const d of decor) {
      g.setLineDash([]);
      if (d.kind === "wedge" || d.kind === "lane") {
        g.globalAlpha = 0.07; g.fillStyle = v("--router"); g.beginPath();
        if (d.kind === "wedge") { g.moveTo(sx(d.x), sy(d.y)); g.arc(sx(d.x), sy(d.y), d.r * z, d.a0, d.a1); g.closePath(); }
        else g.roundRect(sx(d.x), sy(d.y), d.w * z, d.h * z, 8 * z);
        g.fill(); g.globalAlpha = 1;
      } else if (opts.rings) {
        const outer = d.kind === "outer" || d.outer;
        g.strokeStyle = v(outer ? "--idle" : "--border"); g.lineWidth = 1;
        g.setLineDash(outer ? [2, 5] : []);
        g.beginPath();
        if (d.kind === "tick") { g.moveTo(sx(d.x), sy(d.y0)); g.lineTo(sx(d.x), sy(d.y1)); }
        else g.arc(sx(d.x), sy(d.y), d.r * z, 0, 2 * Math.PI);
        g.stroke();
      }
    }
    g.setLineDash([]);
  }

  // Names keep one size on screen; one that would cover another waits until there is room.
  function drawLabels(g, z, sx, sy) {
    const font = v("--font-sans") || "system-ui, sans-serif";
    const taken = [];
    const W = cy.width(), H = cy.height();
    const put = (lines, x, y, force) => {
      let w = 0, h = 0;
      for (const l of lines) { g.font = l.font; w = Math.max(w, g.measureText(l.text).width); h += l.h; }
      const b = [x - w / 2 - 2, y, x + w / 2 + 2, y + h];
      if (b[2] < 0 || b[0] > W || b[3] < 0 || b[1] > H) return;
      if (!force && taken.some((t) => b[0] < t[2] && t[0] < b[2] && b[1] < t[3] && t[1] < b[3])) return;
      taken.push(b);
      g.textAlign = "center"; g.textBaseline = "top"; g.lineJoin = "round"; g.lineWidth = 3; g.strokeStyle = v("--surface");
      for (const l of lines) { g.font = l.font; g.fillStyle = l.color; g.strokeText(l.text, x, y); g.fillText(l.text, x, y); y += l.h; }
    };
    const text = (t, color = "--text", px = 11, weight = 400) => ({ text: t, color: v(color), font: `${weight} ${px}px ${font}`, h: px + 2 });

    cy.nodes("[kind = 'net']").forEach((n) => {
      const ds = decor.filter((d) => d.network === n.data("network") && (d.kind === "outer" || d.kind === "lane" || d.kind === "wedge"));
      let x, y;
      if (ds.some((d) => d.kind === "lane")) { x = Math.min(...ds.map((d) => d.x)) + 60; y = Math.min(...ds.map((d) => d.y)) - 24; }
      else if (ds.length) { x = ds[0].x; y = Math.min(...ds.map((d) => d.y - d.r)) - 34; }
      else { const b = n.boundingBox(); x = (b.x1 + b.x2) / 2; y = b.y1 - 20; }
      put([text(n.data("label"), "--accent", 13, 600)], sx(x), sy(y), true);
    });

    const rank = (n) => (trunkEnd(n) && n.data("kind") !== "hop" ? 0 : n.selected() || n.id() === hover ? 1 : 2);
    const nodes = cy.nodes("[kind != 'net']").not(".off").toArray().sort((a, b) => rank(a) - rank(b) || a.id().localeCompare(b.id()));
    for (const n of nodes) {
      const d = n.data(), lines = [], r = rank(n);
      if (opts.names || r < 2) lines.push(text(d.label, "--text", r === 0 ? 12 : 11, r === 0 ? 600 : 400));
      if (opts.ips && d.ip && d.ip !== d.label) lines.push(text(d.ip, "--muted", 10));
      if (opts.ms && d.rtt && d.status !== "offline") lines.push(text(Object.values(d.rtt).map((x) => fmtMs(x.ms)).join(" / "), "--muted", 10));
      const p = n.position();
      if (lines.length) put(lines, sx(p.x), sy(p.y) + (n.height() / 2) * z + 5, r < 2);
    }
    if (opts.ports) {
      if (opts.layout === "radar") {
        cy.edges().forEach((e) => {
          if (!e.data("label") || e.source().hasClass("off") || e.target().hasClass("off")) return;
          const a = e.source().position(), b = e.target().position();
          put([text(e.data("label"), "--muted", 10)], sx(a.x + (b.x - a.x) * 0.55), sy(a.y + (b.y - a.y) * 0.55) - 6);
        });
      } else for (const d of decor) {
        if (d.kind === "wedge") { const m = (d.a0 + d.a1) / 2, r = d.r + 22; put([text(d.label, "--router", 11, 600)], sx(d.x + r * Math.cos(m)), sy(d.y + r * Math.sin(m)) - 6); }
        else if (d.kind === "lane") put([text(d.label, "--router", 11, 600)], sx(d.x) + 34, sy(d.y) + 3);
      }
    }
    if (opts.rings) for (const d of decor) {
      if (d.kind === "ring") put([text(d.label, "--muted", 10)], sx(d.x + d.r * Math.cos(-1.2)), sy(d.y + d.r * Math.sin(-1.2)) - 6);
      else if (d.kind === "tick") put([text(d.label, "--muted", 10)], sx(d.x), sy(d.y1) + 2);
    }
  }

  function paint() {
    const z = cy.zoom(), pan = cy.pan(), sx = (x) => x * z + pan.x, sy = (y) => y * z + pan.y;
    drawDecor(surface("decor"), z, sx, sy);
    drawLabels(surface("labels"), z, sx, sy);
  }
  let queued = false;
  const repaint = () => { if (queued) return; queued = true; requestAnimationFrame(() => { queued = false; paint(); }); };
  cy.on("render", repaint);
  cy.on("mouseover", "node", (e) => { hover = e.target.id(); repaint(); });
  cy.on("mouseout", "node", () => { hover = null; repaint(); });
  matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => { cy.style(graphStyle()); repaint(); });

  let seq = 0;
  // load fetches the graph and the ping jobs; a newer load wins over a slower older one.
  async function load({ fresh = true } = {}) {
    const mine = ++seq, network = netSel.value;
    const [graph, jobs] = await Promise.all([
      api("GET", "/api/graph" + (network ? "?network=" + encodeURIComponent(network) : "")),
      api("GET", "/api/jobs").catch(() => []),
    ]);
    if (mine !== seq) return;
    els = MapLayout.prepare(graph);
    const open = current && els.some((e) => e.data.id === current.id());
    if (current && !open) { panel.hidden = true; current = null; }
    apply({ animate: !fresh, refit: fresh });
    if (current) current = cy.getElementById(current.id());
    showNotice(jobs, network);
    if (fresh && !graph.length) toast("No devices yet — waiting for the first scan");
  }
  const reload = () => load().catch((err) => toast("Could not load graph: " + err.message));

  function showNotice(jobs, network) {
    const mine = jobs.filter((j) => !network || j.network === network);
    const failed = mine.filter((j) => j.source.startsWith("ping:") && j.lastErr);
    const measured = els.some((e) => e.data.rtt);
    const waiting = !measured && mine.some((j) => j.source.startsWith("ping:") || j.source === "nmap");
    notice.textContent = failed.length ? failed.map((j) => `Ping times for ${j.network} are not updating: ${j.lastErr}`).join(" · ")
      : waiting ? "Waiting for the first ping run" : "";
    notice.hidden = !notice.textContent;
  }

  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const ago = (t) => { if (!t || t.startsWith("0001")) return "never"; const d = (Date.now() - new Date(t)) / 1000;
    return d < 60 ? Math.round(d) + "s ago" : d < 3600 ? Math.round(d / 60) + "m ago" : d < 172800 ? Math.round(d / 3600) + "h ago" : new Date(t).toLocaleDateString(); };

  const canEdit = panel.dataset.canEdit === "true";
  let current = null; // node shown in the panel
  const ext = (href, text) => `<a class="btn btn-sm" href="${esc(href)}" target="_blank" rel="noopener noreferrer">${esc(text)} ↗</a>`;
  const hostOf = (ip) => (ip.includes(":") ? `[${ip}]` : ip);
  const ipsOf = (d) => (d.members && d.members.length ? d.members.map((m) => m.ip) : String(d.ip || "").split(", ")).filter(Boolean);

  // webLinks returns the web interfaces a detailed scan found ({ip, ports}), or http://<ip> for each
  // address when none were found.
  function webLinks(scanned, ips) {
    const out = [];
    for (const { ip, ports } of scanned) {
      for (const p of ports || []) {
        const svc = (p.service || "").toLowerCase();
        const tls = /https|ssl|tls/.test(svc) || [443, 8443, 9443].includes(p.port);
        if (p.proto !== "tcp" || !(/http/.test(svc) || [80, 443, 8080, 8443].includes(p.port))) continue;
        out.push(`${tls ? "https" : "http"}://${hostOf(ip)}${p.port === (tls ? 443 : 80) ? "" : ":" + p.port}/`);
      }
    }
    if (!out.length) ips.forEach((ip) => out.push(`http://${hostOf(ip)}/`));
    return [...new Set(out)];
  }
  const openLinks = (links) => links.map((u) => ext(u, "Open " + u.replace(/\/$/, ""))).join("");

  async function copyText(text) {
    try { await navigator.clipboard.writeText(text); return; } catch { /* plain http: no clipboard API */ }
    const t = document.createElement("textarea");
    t.value = text; document.body.append(t); t.select(); document.execCommand("copy"); t.remove();
  }

  // One listener for every panel action button.
  panel.addEventListener("click", async (e) => {
    const b = e.target.closest("[data-act]");
    if (!b || !current) return;
    const ref = { network: b.dataset.network, key: b.dataset.key };
    try {
      if (b.dataset.act === "copy") {
        await copyText(b.dataset.ip); toast("Copied " + b.dataset.ip);
      } else if (b.dataset.act === "forget") {
        if (!confirm(`Forget ${ref.key} in ${ref.network}? It comes back if a scan sees it again.`)) return;
        await api("DELETE", "/api/devices/" + encodeURIComponent(ref.key) + "?network=" + encodeURIComponent(ref.network));
        toast("Device forgotten"); panel.hidden = true; await reload();
      } else if (b.dataset.act === "split") {
        await api("POST", "/api/machines/split", ref);
        toast("Split off from the machine"); panel.hidden = true; await reload();
      }
    } catch (err) { toast("Failed: " + err.message); }
  });

  // One line per network that measured the device.
  function pingText(d) {
    const list = Object.entries(d.status === "offline" ? {} : d.rtt || {});
    if (!list.length) return "no reply";
    return list.map(([n, x]) => `${esc(fmtMs(x.ms))} from ${esc(x.origin)}${list.length > 1 ? " (" + esc(n) + ")" : ""}, ${esc(ago(x.at))}`).join("<br>");
  }

  function show(n) {
    current = n;
    const d = n.data();
    if (d.kind === "net") {
      const kids = n.children();
      const on = kids.filter((c) => c.data("status") === "online").length;
      panel.innerHTML = `<button class="btn btn-sm close float-right">×</button><h2>${esc(d.label)}</h2>
        <p class="my-2 text-muted">${kids.length} nodes · ${on} online</p>
        <div class="mt-2 mb-1 flex flex-wrap gap-1.5"><button class="btn btn-sm" type="button" data-scan="${esc(d.network)}">Scan now</button>
        <a class="btn btn-sm" href="/map?network=${encodeURIComponent(d.network)}">Only this network</a>
        ${ext("/map?network=" + encodeURIComponent(d.network), "New tab")}</div>`;
    } else if (d.kind !== "device") {
      panel.innerHTML = `<button class="btn btn-sm close float-right">×</button><h2>${esc(d.label)}</h2><p class="my-2 text-muted">${esc(d.kind === "hop" ? "router hop (from traceroute)" : d.kind === "netmon" ? "ping times here are measured by netmon" : d.kind)}${d.network ? " in " + esc(d.network) : ""} · ${n.degree()} links</p>` +
        (d.ip ? `<div class="mt-2 mb-1 flex flex-wrap gap-1.5">${openLinks([`http://${hostOf(d.ip)}/`])}
          <button class="btn btn-sm" type="button" data-act="copy" data-ip="${esc(d.ip)}">Copy IP</button></div>` : "");
    } else {
      const members = d.members || [];
      const ref = (m) => `data-network="${esc(m.network)}" data-key="${esc(m.key)}"`;
      const memberList = members.length > 1 ? `<h3 class="sub">Addresses</h3><table class="mini-table"><tbody>${members.map((m) =>
        `<tr><td>${esc(m.network)}</td><td class="mono">${esc(m.ip)}</td><td class="mono text-xs">${esc(m.mac)}</td><td><span class="pill ${m.online ? "bg-ok" : ""}"></span></td></tr>
        ${canEdit ? `<tr><td class="space-x-1 pb-1" colspan="4">${d.machine ? `<button class="btn btn-sm" type="button" data-act="split" ${ref(m)} title="Remove this address from the machine">Split</button>` : ""}
          <button class="btn btn-sm btn-danger" type="button" data-act="forget" ${ref(m)} title="Remove this address; it comes back if seen again">Forget</button></td></tr>` : ""}`).join("")}</tbody></table>` : "";
      const ip = ipsOf(d)[0] || "";
      const actions = `<h3 class="sub">Actions</h3><div class="mt-2 mb-1 flex flex-wrap gap-1.5"><span class="open-links contents">${openLinks(webLinks([], ipsOf(d)))}</span>
        ${ip ? `<button class="btn btn-sm" type="button" data-act="copy" data-ip="${esc(ip)}">Copy IP</button>
        ${ext("/devices?q=" + encodeURIComponent(ip), "View in Devices")}` : ""}
        ${canEdit && members.length <= 1 ? `<button class="btn btn-sm btn-danger" type="button" data-act="forget" ${ref(members[0] || { network: d.network, key: d.key })}>Forget</button>` : ""}</div>`;
      panel.innerHTML = `<button class="btn btn-sm close float-right">×</button>
        <h2>${esc(d.label)}</h2>${actions}
        <dl class="my-2.5 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[13px]"><dt class="text-muted">Status</dt><dd class="min-w-0 break-all">${esc(d.status)}</dd><dt class="text-muted">IP</dt><dd class="mono min-w-0 break-all">${esc(d.ip)}</dd>
        <dt class="text-muted">MAC</dt><dd class="mono min-w-0 break-all">${esc(d.mac)}</dd><dt class="text-muted">Vendor</dt><dd class="min-w-0 break-all">${esc(d.vendor)}</dd>
        <dt class="text-muted">Hostname</dt><dd class="min-w-0 break-all">${esc(d.hostname)}</dd><dt class="text-muted">Port</dt><dd class="mono min-w-0 break-all">${esc(d.port)}</dd>
        <dt class="text-muted">Network</dt><dd class="min-w-0 break-all">${esc(d.network)}</dd><dt class="text-muted">Ping</dt><dd class="min-w-0 break-words">${pingText(d)}</dd><dt class="text-muted">Last seen</dt><dd class="min-w-0 break-all">${esc(ago(d.lastSeen))}</dd>
        <dt class="text-muted">First seen</dt><dd class="min-w-0 break-all">${esc(ago(d.firstSeen))}</dd></dl>${memberList}
        <div class="details-box"><span class="text-muted text-xs">Loading services…</span></div>
        <form class="mt-3 grid gap-2"><input class="label control" name="label" placeholder="Label" value="${esc(n.data("userLabel") || "")}" aria-label="Label">
        <label class="check-label"><input type="checkbox" name="known" ${d.known ? "checked" : ""}> known device</label>
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
          repaint();
          toast("Saved");
        } catch (err) { toast("Save failed: " + err.message); }
      });
    }
    if (d.kind === "device") loadDetails(d);
    panel.querySelector(".close").addEventListener("click", () => { panel.hidden = true; cy.elements().unselect(); current = null; });
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
    const scanned = members.filter((m) => byRef.has(m.network + "|" + m.key)).map((m) => ({ ip: m.ip, ports: byRef.get(m.network + "|" + m.key).ports }));
    const links = panel.querySelector(".open-links");
    if (links) links.innerHTML = openLinks(webLinks(scanned, members.map((m) => m.ip).filter(Boolean)));
    box.innerHTML = `<h3 class="sub">Services</h3>` + members.map((m) => {
      const x = byRef.get(m.network + "|" + m.key);
      const head = members.length > 1 ? `<div class="text-xs mono">${esc(m.ip)} <span class="text-muted">${esc(m.network)}</span></div>` : "";
      const body = !x ? `<div class="text-muted text-xs">not scanned yet</div>`
        : (x.os ? `<div class="text-xs">OS: ${esc(x.os)} <span class="text-muted">(${x.osAccuracy}%)</span></div>` : "") +
          (x.ports.length ? `<table class="mini-table"><tbody>${x.ports.map((p) => `<tr><td class="mono">${p.port}/${esc(p.proto)}</td><td>${esc(p.service)}</td><td class="text-xs">${esc(p.product)} ${esc(p.version)}</td></tr>`).join("")}</tbody></table>`
            : `<div class="text-muted text-xs">no open ports</div>`) +
          `<div class="text-muted text-xs">scanned ${esc(ago(x.scannedAt))}</div>`;
      return `<div class="mt-1.5 mb-2.5">${head}${body}
        <button class="btn btn-sm" type="button" data-scan-net="${esc(m.network)}" data-scan-key="${esc(m.key)}">${x ? "Rescan" : "Scan details now"}</button></div>`;
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
  cy.on("tap", (e) => { if (e.target === cy) { panel.hidden = true; current = null; } });
  netSel.addEventListener("change", () => {
    const u = new URL(location); netSel.value ? u.searchParams.set("network", netSel.value) : u.searchParams.delete("network");
    history.replaceState(null, "", u); reload();
  });

  layoutSel.value = opts.layout;
  layoutSel.addEventListener("change", () => {
    opts.layout = layoutSel.value; MapOptions.save(storage, opts); apply({ refit: true });
  });
  const display = document.getElementById("display");
  display.querySelectorAll("[data-opt]").forEach((box) => {
    box.checked = opts[box.dataset.opt];
    box.addEventListener("change", () => {
      opts[box.dataset.opt] = box.checked; MapOptions.save(storage, opts); apply({ animate: true });
    });
  });
  document.addEventListener("click", (e) => { if (!display.contains(e.target)) display.open = false; });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && display.open) { display.open = false; display.querySelector("summary").focus(); }
  });
  document.getElementById("relayout").addEventListener("click", () => apply({ refit: true }));
  new ResizeObserver(() => { cy.resize(); repaint(); }).observe(document.getElementById("cy"));
  reload();
  setInterval(() => load({ fresh: false }).catch(() => { /* ignore transient errors */ }), 60000);
})();
