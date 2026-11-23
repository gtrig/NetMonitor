// Settings page: edits networks, routers, SNMP targets, channels, rules and workers, then PUTs them.
(() => {
  const MASKED = "••••••";
  const root = document.getElementById("settings");
  const saveBtn = document.getElementById("save");
  const errorsEl = document.getElementById("errors");
  let state = null;      // editable settings
  let meta = {};         // can_edit, event_types, path
  const bar = document.getElementById("savebar");
  const staleEl = document.getElementById("stale");
  const GROUPS = { networks: ["networks"], sources: ["routers", "snmp_targets"], notifications: ["channels", "rules"], system: ["workers"] };
  const KINDS = Object.values(GROUPS).flat();
  const LISTS = KINDS.filter((k) => k !== "workers");
  const groupOf = (kind) => Object.keys(GROUPS).find((g) => GROUPS[g].includes(kind));
  const open = new Set();  // expanded items (objects in state)
  let saved = {};          // JSON of each kind as last loaded
  let typing = null;       // group of a field being typed in, before its change event
  let saving = false;

  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const changed = (group) => !!state && (typing === group || GROUPS[group].some((k) => JSON.stringify(state[k]) !== saved[k]));
  const isDirty = () => Object.keys(GROUPS).some(changed);
  window.addEventListener("beforeunload", (e) => { if (isDirty()) e.preventDefault(); });

  // Tab counts, unsaved dots and the save bar.
  function refreshChrome() {
    for (const g of Object.keys(GROUPS)) {
      const tab = document.getElementById(`tab-${g}`);
      if (g !== "system") { // its count is the stale data
        const count = tab.querySelector(".tab-count");
        count.hidden = !state;
        count.textContent = state ? GROUPS[g].reduce((n, k) => n + state[k].length, 0) : "";
      }
      tab.querySelector(".tab-dot").hidden = !changed(g);
    }
    bar.hidden = !isDirty();
    saveBtn.disabled = saving || !meta.can_edit;
    saveBtn.textContent = saving ? "Saving…" : "Save changes";
  }

  // ---- tabs ----
  const tabs = [...document.querySelectorAll("#tabs [role=tab]")];
  function showTab(name) {
    if (!tabs.some((t) => t.dataset.tab === name)) name = "networks";
    for (const t of tabs) {
      const on = t.dataset.tab === name;
      t.setAttribute("aria-selected", on);
      t.tabIndex = on ? 0 : -1;
      document.getElementById(t.getAttribute("aria-controls")).hidden = !on;
      if (on) t.scrollIntoView({ block: "nearest", inline: "nearest" });
    }
  }
  function pickTab(t) { history.replaceState(null, "", "#" + t.dataset.tab); showTab(t.dataset.tab); }
  for (const t of tabs) t.addEventListener("click", () => pickTab(t));
  document.getElementById("tabs").addEventListener("keydown", (e) => {
    const i = tabs.indexOf(document.activeElement);
    const to = { ArrowRight: i + 1, ArrowLeft: i - 1 + tabs.length, Home: 0, End: tabs.length - 1 }[e.key];
    if (i < 0 || to === undefined) return;
    e.preventDefault();
    const t = tabs[to % tabs.length];
    t.focus(); pickTab(t);
  });
  window.addEventListener("hashchange", () => showTab(location.hash.slice(1)));
  showTab(location.hash.slice(1));

  async function load() {
    const res = await fetch(url("/api/settings"));
    if (!res.ok) { document.getElementById("loading").textContent = `Could not load settings: ${await res.text()}`; return; }
    const data = await res.json();
    const key = (kind, item, i) => `${kind}/${item.name ?? i}`;
    const wasOpen = new Set();
    if (state) for (const k of LISTS) state[k].forEach((it, i) => { if (open.has(it)) wasOpen.add(key(k, it, i)); });
    meta = { can_edit: data.can_edit, event_types: data.event_types, path: data.path };
    state = Object.fromEntries(LISTS.map((k) => [k, data[k] || []]));
    state.workers = data.workers || {};
    for (const n of state.networks) { n.sources = n.sources || []; n.exclude = n.exclude || []; }
    open.clear();
    for (const k of KINDS) {
      saved[k] = JSON.stringify(state[k]);
      if (k !== "workers") state[k].forEach((it, i) => { if (wasOpen.has(key(k, it, i))) open.add(it); });
    }
    typing = null;
    document.getElementById("loading").hidden = true;
    document.getElementById("banner").hidden = meta.can_edit;
    document.getElementById("cfgpath").textContent = meta.path;
    render();
  }

  // ---- field builders (return HTML; inputs carry data-path into state) ----
  const ro = () => (meta.can_edit ? "" : "disabled");
  function text(path, label, value, opts = {}) {
    return `<label class="field ${opts.cls || ""}"><span>${esc(label)}</span>
      <input data-path="${path}" value="${esc(value)}" placeholder="${esc(opts.ph || "")}" ${opts.attrs || ""} ${opts.type ? `type="${opts.type}"` : ""} ${ro()}></label>`;
  }
  function check(path, label, value) {
    return `<label class="field field-check"><input type="checkbox" data-path="${path}" ${value ? "checked" : ""} ${ro()}><span>${esc(label)}</span></label>`;
  }
  function select(path, label, value, options, opts = {}) {
    const o = options.map((v) => `<option value="${esc(v)}" ${v === value ? "selected" : ""}>${esc(v || opts.empty || "—")}</option>`).join("");
    return `<label class="field ${opts.cls || ""}"><span>${esc(label)}</span><select data-path="${path}" ${opts.restructure ? "data-restructure" : ""} ${ro()}>${o}</select></label>`;
  }
  function secret(path, label, value) {
    const isRef = /^\$\{[A-Za-z_][A-Za-z0-9_]*\}$/.test(value || "");
    const set = value === MASKED;
    const ph = set ? "set — leave empty to keep" : "value or ${ENV_NAME}";
    return `<label class="field"><span>${esc(label)} ${isRef ? '<em class="not-italic">from .env</em>' : set ? '<em class="not-italic">set</em>' : ""}</span>
      <input data-path="${path}" data-secret type="${isRef ? "text" : "password"}" autocomplete="new-password"
        value="${isRef ? esc(value) : ""}" placeholder="${esc(ph)}" ${ro()}></label>`;
  }
  const add = (kind, label) => meta.can_edit ? `<button class="btn btn-sm" data-add="${kind}" type="button">${label}</button>` : "";

  // Summary line of a collapsed item: title and a short description (HTML).
  const tag = (t) => `<span class="tag">${esc(t)}</span>`;
  const list = (a) => esc(a.join(", "));
  const summaries = {
    networks: (n) => [n.name || "New network", `<span class="mono">${esc(n.cidr)}</span> ${n.sources.map((s) => tag(s.type)).join(" ")}`],
    routers: (r) => [r.name || "New router", `<span class="mono">${esc(r.address)}</span> ${tag("MikroTik")}`],
    snmp_targets: (t) => [t.name || "New SNMP target", `<span class="mono">${esc(t.address)}</span> ${tag(`SNMP ${t.version || "v2c"}`)}`],
    channels: (c) => [c.name || "New channel", tag(c.type)],
    rules: (r, i) => [`Rule ${i + 1}`, `${!r.events?.length ? "All events" : r.events.length === 1 ? esc(r.events[0]) : `${r.events.length} events`}
      ${r.networks?.length ? `from ${list(r.networks)}` : "from all networks"} ${r.channels?.length ? `to ${list(r.channels)}` : "to no channel"}`],
  };
  function summary(kind, i) {
    i = +i; // may come from a data attribute
    const [title, desc] = summaries[kind](state[kind][i], i);
    return `<b class="text-fg">${esc(title)}</b><span class="item-desc">${desc}</span>`;
  }
  function item(kind, i, body, actions = "") {
    const foot = meta.can_edit ? `<div class="mt-4 flex justify-end gap-2 border-t pt-3">${actions}<button class="btn btn-sm btn-danger" data-del="${kind}" data-i="${i}" type="button">Delete</button></div>` : "";
    return `<details class="item" data-kind="${kind}" data-i="${i}" ${open.has(state[kind][i]) ? "open" : ""}>
      <summary>${summary(kind, i)}</summary><div class="px-4 pt-1 pb-4">${body}${foot}</div></details>`;
  }

  function section(kind, title, addLabel, items, hint, empty) {
    root.querySelector(`[data-section="${kind}"]`).innerHTML = `<div class="flex flex-wrap items-center gap-3"><h2 class="mb-0 flex-1">${title}</h2>${add(kind, addLabel)}</div>
      ${hint ? `<p class="mt-1 max-w-[75ch] text-xs text-muted">${hint}</p>` : ""}
      <div class="item-list mt-3">${items || `<p class="px-4 py-5 text-muted">${empty}</p>`}</div>`;
  }

  function renderNetworks() {
    const routerNames = state.routers.map((r) => r.name);
    const snmpNames = state.snmp_targets.map((t) => t.name);
    return state.networks.map((n, i) => {
      const sources = n.sources.map((s, j) => {
        const p = `networks.${i}.sources.${j}`;
        const refs = s.type === "router" ? routerNames : s.type === "snmp" ? snmpNames : [];
        return `<div class="flex w-full flex-wrap items-end gap-2">
          ${select(`${p}.type`, "Source", s.type, ["router", "snmp", "nmap", "services"], { restructure: true, cls: "w-[120px]" })}
          ${s.type === "nmap" || s.type === "services" ? text(`${p}.args`, "Extra nmap args", (s.args || []).join(" "), { ph: s.type === "services" ? "-p 1-65535" : "-T4", cls: "min-w-40 max-w-[360px] flex-1" })
            : select(`${p}.ref`, s.type === "router" ? "Router" : "SNMP target", s.ref || "", ["", ...refs], { empty: "choose…", cls: "min-w-40 max-w-[360px] flex-1" })}
          ${text(`${p}.interval`, "Every", s.interval, { ph: s.type === "services" ? "6h" : "5m", cls: "w-[90px]" })}
          ${s.type === "nmap" ? check(`${p}.traceroute`, "Traceroute", s.traceroute) : ""}
          ${meta.can_edit ? `<button class="btn btn-sm btn-danger" data-del-source="${i}" data-j="${j}" type="button" aria-label="Remove source">×</button>` : ""}
        </div>`;
      }).join("");
      return item("networks", i, `
        <div class="grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-x-3.5 gap-y-2.5">
          ${text(`networks.${i}.name`, "Name", n.name)}
          ${text(`networks.${i}.cidr`, "CIDR", n.cidr, { ph: "192.168.1.0/24" })}
          ${text(`networks.${i}.exclude`, "Exclude IPs", n.exclude.join(", "), { ph: "comma separated" })}
          ${text(`networks.${i}.offline_after`, "Offline after", n.offline_after, { ph: "auto (3 missed scans)" })}
        </div>
        ${check(`networks.${i}.auto_details`, "Scan new devices in detail automatically (open ports, services, OS)", n.auto_details ?? true)}
        <div class="mt-3 grid justify-items-start gap-2"><span class="sub">Discovery sources</span>${sources}
          ${meta.can_edit ? `<button class="btn btn-sm" data-add-source="${i}" type="button">Add source</button>` : ""}</div>`);
    }).join("");
  }

  function renderRouters() {
    return state.routers.map((r, i) => item("routers", i, `
      <div class="grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-x-3.5 gap-y-2.5">
        ${text(`routers.${i}.name`, "Name", r.name)}
        ${text(`routers.${i}.address`, "Address", r.address, { ph: "192.168.88.1:8729" })}
        ${text(`routers.${i}.username`, "Username", r.username, { ph: "netmon-ro" })}
        ${secret(`routers.${i}.password`, "Password", r.password)}
        ${check(`routers.${i}.tls`, "TLS (api-ssl)", r.tls)}
        ${check(`routers.${i}.insecure_skip_verify`, "Accept self-signed certificate", r.insecure_skip_verify)}
      </div>`)).join("");
  }

  function renderSNMP() {
    return state.snmp_targets.map((t, i) => {
      const v3 = t.version === "v3";
      return item("snmp_targets", i, `
      <div class="grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-x-3.5 gap-y-2.5">
        ${text(`snmp_targets.${i}.name`, "Name", t.name)}
        ${text(`snmp_targets.${i}.address`, "Address", t.address, { ph: "192.168.88.1" })}
        ${text(`snmp_targets.${i}.port`, "Port", t.port || "", { ph: "161", type: "number" })}
        ${select(`snmp_targets.${i}.version`, "Version", t.version || "v2c", ["v2c", "v3"], { restructure: true })}
        ${v3 ? `${text(`snmp_targets.${i}.username`, "Username", t.username)}
          ${select(`snmp_targets.${i}.auth_proto`, "Auth", t.auth_proto || "SHA", ["SHA", "SHA256", "SHA512", "MD5"])}
          ${secret(`snmp_targets.${i}.auth_pass`, "Auth passphrase", t.auth_pass)}
          ${select(`snmp_targets.${i}.priv_proto`, "Privacy", t.priv_proto || "AES", ["AES", "DES"])}
          ${secret(`snmp_targets.${i}.priv_pass`, "Privacy passphrase", t.priv_pass)}`
          : secret(`snmp_targets.${i}.community`, "Community", t.community)}
      </div>`);
    }).join("");
  }

  function renderChannels() {
    return state.channels.map((c, i) => {
      const p = `channels.${i}`;
      let fields = "";
      if (c.type === "telegram") fields = secret(`${p}.bot_token`, "Bot token", c.bot_token) + text(`${p}.chat_id`, "Chat ID", c.chat_id);
      if (c.type === "discord") fields = secret(`${p}.webhook_url`, "Webhook URL", c.webhook_url);
      if (c.type === "email") fields = text(`${p}.smtp_host`, "SMTP host", c.smtp_host) +
        text(`${p}.smtp_port`, "Port", c.smtp_port || "", { ph: "587", type: "number" }) + check(`${p}.starttls`, "STARTTLS", c.starttls) +
        text(`${p}.username`, "Username", c.username) + secret(`${p}.password`, "Password", c.password) +
        text(`${p}.from`, "From", c.from) + text(`${p}.to`, "To", (c.to || []).join(", "), { ph: "comma separated" });
      const test = meta.can_edit && c.original_name ? `<button class="btn btn-sm" data-test="${esc(c.original_name)}" type="button">Send test</button>` : "";
      return item("channels", i, `
        <div class="grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-x-3.5 gap-y-2.5">${text(`${p}.name`, "Name", c.name)}${select(`${p}.type`, "Type", c.type, ["telegram", "discord", "email"], { restructure: true })}${fields}</div>`, test);
    }).join("");
  }

  function checks(path, all, chosen, emptyMeansAll) {
    const set = new Set(chosen || []);
    return `<div class="checks flex flex-wrap items-center gap-x-3.5 gap-y-1.5">${all.map((v) => `<label class="check-label"><input type="checkbox" data-list="${path}" value="${esc(v)}" ${set.has(v) ? "checked" : ""} ${ro()}>${esc(v)}</label>`).join("")}
      ${emptyMeansAll ? `<span class="checks-all text-muted text-xs" ${set.size ? "hidden" : ""}>(none ticked = all)</span>` : ""}</div>`;
  }

  function renderRules() {
    return state.rules.map((r, i) => item("rules", i, `
      <div class="grid gap-1.5"><span class="sub">Events</span>${checks(`rules.${i}.events`, meta.event_types, r.events, true)}
      <span class="sub">Networks</span>${checks(`rules.${i}.networks`, state.networks.map((n) => n.name), r.networks, true)}
      <span class="sub">Send to</span>${checks(`rules.${i}.channels`, state.channels.map((c) => c.name), r.channels, false)}
      ${check(`rules.${i}.only_unknown`, "Only devices not marked as known", r.only_unknown)}</div>`)).join("");
  }

  function renderWorkers() {
    root.querySelector('[data-section="workers"]').innerHTML = `<h2>Scan workers</h2><div class="card">
      <div class="grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-x-3.5 gap-y-2.5">
        ${text("workers.scans", "Parallel scans", state.workers.scans || "", { ph: "4", type: "number", attrs: 'min="2" max="64"' })}
        ${text("workers.detailed", "Parallel detailed scans", state.workers.detailed || "", { ph: "2", type: "number", attrs: 'min="1" max="63"' })}
      </div>
      <p class="mt-2 max-w-[75ch] text-xs text-muted">How many scans run at once; the rest wait in the queue on the Jobs page. Detailed scans are slow and use up to their own limit, which must be lower so a worker is always free to check who is online. Empty uses the default. Saving restarts the scans that are running.</p></div>`;
  }

  const renderers = {
    workers: renderWorkers,
    networks: () => section("networks", "Networks", "Add network", renderNetworks(), "Each network is scanned by its sources; hosts outside the CIDR are ignored. nmap finds who is online; services runs a slow detailed scan (open ports, service versions, OS) of the hosts that are online.", "No networks yet. Add one to start discovering devices."),
    routers: () => section("routers", "Routers", "Add router", renderRouters(), "MikroTik routers, read over the RouterOS API. Use a read-only API user. Secrets can be written as ${NAME} to keep them in .env.", "No routers yet."),
    snmp_targets: () => section("snmp_targets", "SNMP targets", "Add SNMP target", renderSNMP(), "", "No SNMP targets yet."),
    channels: () => section("channels", "Channels", "Add channel", renderChannels(), "Where alerts are sent. Save before sending a test to a new or changed channel.", "No channels yet. Add one to receive alerts."),
    rules: () => section("rules", "Rules", "Add rule", renderRules(), "Which changes are sent to which channels. Changes from one scan are batched into one message per channel.", "No rules yet. Without a rule, nothing is sent."),
  };
  function render(kinds = KINDS) {
    for (const k of kinds) renderers[k]();
    refreshChrome();
  }
  function refreshSummary(kind, i) {
    const el = root.querySelector(`details[data-kind="${kind}"][data-i="${i}"] > summary`);
    if (el) el.innerHTML = summary(kind, i);
  }
  // Sections that show the names of another section's items.
  const dependents = { networks: ["rules"], routers: ["networks"], snmp_targets: ["networks"], channels: ["rules"], rules: [], workers: [] };

  // ---- state updates ----
  function setPath(path, value) {
    const keys = path.split(".");
    let o = state;
    for (const k of keys.slice(0, -1)) o = o[k];
    o[keys.at(-1)] = value;
  }
  function getPath(path) { return path.split(".").reduce((o, k) => o?.[k], state); }

  // Keep references consistent when something is renamed.
  function cascadeRename(path, oldName, newName) {
    if (!oldName || oldName === newName) return;
    const [kind] = path.split(".");
    for (const n of state.networks) for (const s of n.sources) {
      if (kind === "routers" && s.type === "router" && s.ref === oldName) s.ref = newName;
      if (kind === "snmp_targets" && s.type === "snmp" && s.ref === oldName) s.ref = newName;
    }
    for (const r of state.rules) {
      if (kind === "channels" && r.channels) r.channels = r.channels.map((c) => (c === oldName ? newName : c));
      if (kind === "networks" && r.networks) r.networks = r.networks.map((c) => (c === oldName ? newName : c));
    }
  }

  const listFields = /\.(exclude|to)$/;
  const numFields = /\.(port|smtp_port|scans|detailed)$/;
  // Re-render after focus has moved (e.g. Tab), then put focus back on the same field.
  function renderKeepingFocus(kinds) {
    setTimeout(() => {
      const path = document.activeElement?.dataset?.path;
      render(kinds);
      if (path) root.querySelector(`[data-path="${CSS.escape(path)}"]`)?.focus();
    });
  }

  root.addEventListener("change", (e) => {
    const el = e.target;
    if (el.dataset.list) {
      const set = new Set(getPath(el.dataset.list) || []);
      el.checked ? set.add(el.value) : set.delete(el.value);
      setPath(el.dataset.list, [...set]);
      const [kind, i] = el.dataset.list.split(".");
      const hint = el.closest(".checks").querySelector(".checks-all");
      if (hint) hint.hidden = set.size > 0;
      refreshSummary(kind, i); refreshChrome(); return;
    }
    const path = el.dataset.path;
    if (!path) return;
    let v = el.type === "checkbox" ? el.checked : el.value.trim();
    if (listFields.test(path)) v = v ? v.split(",").map((x) => x.trim()).filter(Boolean) : [];
    else if (path.endsWith(".args")) v = v ? v.split(/\s+/) : [];
    else if (numFields.test(path)) v = v ? Number(v) : 0;
    else if ("secret" in el.dataset && v === "") v = getPath(path) === MASKED ? MASKED : getPath(path) || "";
    if (path.endsWith(".name")) cascadeRename(path, getPath(path), v);
    if (path.endsWith(".type") && path.includes(".sources.")) {
      setPath(path.replace(/type$/, "ref"), "");
      if (v !== "nmap") setPath(path.replace(/type$/, "traceroute"), false);
      if (v !== "nmap" && v !== "services") setPath(path.replace(/type$/, "args"), []);
      if (v === "services") setPath(path.replace(/type$/, "interval"), "6h");
      else if (getPath(path) === "services") setPath(path.replace(/type$/, "interval"), "15m");
    }
    setPath(path, v);
    typing = null;
    const [kind, i] = path.split(".");
    refreshSummary(kind, i);
    refreshChrome();
    if (el.dataset.restructure !== undefined) renderKeepingFocus([kind]);
    else if (path.endsWith(".name")) renderKeepingFocus(dependents[kind]);
  });
  root.addEventListener("input", (e) => {
    const path = e.target.dataset.path;
    if (path && e.target.matches("input:not([type=checkbox])")) { typing = groupOf(path.split(".")[0]); refreshChrome(); }
  });
  // Runs after change: covers typing that was undone, which fires no change event.
  root.addEventListener("focusout", () => { if (typing) { typing = null; refreshChrome(); } });
  root.addEventListener("toggle", (e) => {
    const { kind, i } = e.target.dataset;
    const it = kind && state[kind][+i];
    if (it) e.target.open ? open.add(it) : open.delete(it);
  }, true);

  const blank = {
    networks: () => ({ name: "", cidr: "", sources: [{ type: "nmap", interval: "15m" }], exclude: [] }),
    routers: () => ({ name: "", type: "mikrotik", address: "", username: "netmon-ro", password: "", tls: true, insecure_skip_verify: true }),
    snmp_targets: () => ({ name: "", address: "", version: "v2c", community: "" }),
    channels: () => ({ name: "", type: "telegram" }),
    rules: () => ({ events: ["device_new"], channels: [], only_unknown: true }),
  };
  root.addEventListener("click", (e) => {
    const t = e.target;
    if (t.dataset.add) {
      const kind = t.dataset.add;
      const it = blank[kind]();
      state[kind].push(it); open.add(it); render();
      const el = root.querySelector(`details[data-kind="${kind}"][data-i="${state[kind].length - 1}"]`);
      el.scrollIntoView({ block: "nearest" });
      el.querySelector("input")?.focus();
    }
    if (t.dataset.del) {
      const item = state[t.dataset.del][+t.dataset.i];
      if (!confirm(`Delete ${item.name || (t.dataset.del === "rules" ? `rule ${+t.dataset.i + 1}` : "this item")}?`)) return;
      state[t.dataset.del].splice(+t.dataset.i, 1); render();
    }
    if (t.dataset.addSource) { state.networks[+t.dataset.addSource].sources.push({ type: "nmap", interval: "15m" }); render(); }
    if (t.dataset.delSource) { state.networks[+t.dataset.delSource].sources.splice(+t.dataset.j, 1); render(); }
  });

  function showErrors(list, title = "Not saved") {
    errorsEl.hidden = !list.length;
    errorsEl.innerHTML = list.length ? `<b>${esc(title)}</b><ul class="mt-1.5 list-disc pl-5">${list.map((l) => `<li>${esc(l)}</li>`).join("")}</ul>` : "";
    if (list.length) errorsEl.scrollIntoView({ behavior: "smooth", block: "start" });
  }

  async function readErrors(res) {
    const body = await res.text().catch(() => "");
    try { return JSON.parse(body).errors || [body]; } catch { return [body || res.statusText]; }
  }

  saveBtn.addEventListener("click", async () => {
    saving = true; refreshChrome();
    try {
      const res = await fetch(url("/api/settings"), { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(state) });
      if (res.status !== 204) return showErrors(await readErrors(res));
      showErrors([]); toast("Saved and applied");
      await load();
      loadStale();
    } catch (err) {
      showErrors([err.message]);
    } finally { saving = false; refreshChrome(); }
  });

  document.getElementById("discard").addEventListener("click", async () => {
    if (!confirm("Discard all unsaved changes?")) return;
    showErrors([]);
    try { await load(); } catch (err) { toast("Failed: " + err.message); }
  });

  document.getElementById("reload").addEventListener("click", async () => {
    if (isDirty() && !confirm("Discard unsaved changes and reload from the file?")) return;
    const res = await fetch(url("/api/settings/reload"), { method: "POST" });
    if (res.status === 204) { showErrors([]); toast("Reloaded from file"); return load().then(loadStale); }
    showErrors(await readErrors(res), "Not reloaded");
  });

  // ---- stored data of networks no longer in the config ----
  async function loadStale() {
    let data;
    try { data = await api("GET", "/api/data/networks"); } catch { return; }
    const stale = data.filter((n) => !n.configured && n.devices > 0);
    const targets = data.filter((n) => n.configured).map((n) => n.name);
    staleEl.hidden = !stale.length;
    const badge = document.querySelector("#tab-system .tab-count");
    badge.hidden = !stale.length;
    badge.textContent = stale.length;
    badge.title = "Stored data from removed networks";
    staleEl.innerHTML = `<div class="mb-1 flex items-center gap-3"><h2 class="mb-0">Stored data from removed networks</h2></div>
      <p class="my-2 text-xs text-muted">Devices kept under network names that are no longer configured (for example after a rename). Merge them into a configured network, or delete them. Events are kept either way.</p>
      <div class="mt-2 grid gap-3">${stale.map((n) => `<article class="card px-3.5 py-3"><div class="mb-2.5 flex items-center gap-2"><b>${esc(n.name)}</b>
        <span class="text-muted text-xs">${n.devices} devices · ${n.events} events</span></div>
        ${meta.can_edit ? `<div class="grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-x-3.5 gap-y-2.5">
          <label class="field"><span>Merge into</span><select data-stale-into="${esc(n.name)}">${targets.map((t) => `<option>${esc(t)}</option>`).join("")}</select></label>
          <button class="btn self-end" type="button" data-stale-merge="${esc(n.name)}" ${targets.length ? "" : "disabled"}>Merge</button>
          <button class="btn btn-danger self-end" type="button" data-stale-delete="${esc(n.name)}">Delete</button></div>` : ""}
      </article>`).join("")}</div>`;
  }

  staleEl.addEventListener("click", async (e) => {
    const t = e.target;
    const path = (n) => "/api/data/networks/" + encodeURIComponent(n);
    try {
      if (t.dataset.staleMerge) {
        const name = t.dataset.staleMerge;
        const into = staleEl.querySelector(`[data-stale-into="${CSS.escape(name)}"]`).value;
        if (!confirm(`Move the devices and events of ${name} into ${into}?`)) return;
        await api("POST", path(name) + "/merge", { into });
        toast(`Merged ${name} into ${into}`);
      } else if (t.dataset.staleDelete) {
        const name = t.dataset.staleDelete;
        if (!confirm(`Delete the stored devices of ${name}? This cannot be undone (events are kept).`)) return;
        await api("DELETE", path(name));
        toast(`Deleted stored data of ${name}`);
      } else return;
      loadStale();
    } catch (err) { toast("Failed: " + err.message); }
  });

  load().then(loadStale);
})();
