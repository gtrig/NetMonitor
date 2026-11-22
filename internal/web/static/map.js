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
      { selector: "node[kind = 'device'][known = false]", style: { "background-color": v("--warn") } },
      { selector: "node[status = 'offline']", style: { "background-color": v("--idle"), "opacity": 0.7 } },
      { selector: "node[kind = 'router']", style: {
        "shape": "round-rectangle", "width": 34, "height": 24, "background-color": v("--router"),
        "font-weight": "bold", "font-size": 12 } },
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

  const layout = () => cy.layout({ name: "cose", animate: false, nodeRepulsion: 9000, idealEdgeLength: 70,
    nodeDimensionsIncludeLabels: true, randomize: true }).run();

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
    if (d.kind !== "device") {
      panel.innerHTML = `<button class="btn small close">×</button><h2>${esc(d.label)}</h2><p class="muted">${esc(d.kind)} · ${n.degree()} links</p>`;
    } else {
      panel.innerHTML = `<button class="btn small close">×</button>
        <h2>${esc(d.label)}</h2>
        <dl><dt>Status</dt><dd>${esc(d.status)}</dd><dt>IP</dt><dd class="mono">${esc(d.ip)}</dd>
        <dt>MAC</dt><dd class="mono">${esc(d.mac)}</dd><dt>Vendor</dt><dd>${esc(d.vendor)}</dd>
        <dt>Hostname</dt><dd>${esc(d.hostname)}</dd><dt>Port</dt><dd class="mono">${esc(d.port)}</dd>
        <dt>Network</dt><dd>${esc(d.network)}</dd><dt>Last seen</dt><dd>${esc(ago(d.lastSeen))}</dd>
        <dt>First seen</dt><dd>${esc(ago(d.firstSeen))}</dd></dl>
        <form><input class="label" name="label" placeholder="Label" value="${esc(n.data("userLabel") || "")}" aria-label="Label">
        <label class="check"><input type="checkbox" name="known" ${d.known ? "checked" : ""}> known device</label>
        <button class="btn">Save</button></form>`;
      panel.querySelector("form").addEventListener("submit", async (e) => {
        e.preventDefault();
        const f = e.target;
        try {
          const fields = { known: f.known.checked };
          if (f.label.value.trim()) fields.label = f.label.value.trim();
          await patchDevice(d.key, fields);
          n.data("known", f.known.checked);
          if (fields.label) n.data("label", fields.label);
          toast("Saved");
        } catch (err) { toast("Save failed: " + err.message); }
      });
    }
    panel.querySelector(".close").addEventListener("click", () => { panel.hidden = true; cy.elements().unselect(); });
    panel.hidden = false;
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
