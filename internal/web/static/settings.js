// Settings page: edits networks, routers, SNMP targets, channels and rules, then PUTs them.
(() => {
  const MASKED = "••••••";
  const root = document.getElementById("settings");
  const saveBtn = document.getElementById("save");
  const errorsEl = document.getElementById("errors");
  let state = null;      // editable settings
  let meta = {};         // can_edit, event_types, path
  let dirty = false;

  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const markDirty = () => { dirty = true; saveBtn.disabled = !meta.can_edit; saveBtn.textContent = "Save changes"; };
  window.addEventListener("beforeunload", (e) => { if (dirty) e.preventDefault(); });

  async function load() {
    const res = await fetch(url("/api/settings"));
    if (!res.ok) { root.innerHTML = `<p class="err">Could not load settings: ${esc(await res.text())}</p>`; return; }
    const data = await res.json();
    meta = { can_edit: data.can_edit, event_types: data.event_types, path: data.path };
    state = {
      networks: data.networks || [], routers: data.routers || [], snmp_targets: data.snmp_targets || [],
      channels: data.channels || [], rules: data.rules || [],
    };
    for (const n of state.networks) { n.sources = n.sources || []; n.exclude = n.exclude || []; }
    document.getElementById("banner").hidden = meta.can_edit;
    document.getElementById("cfgpath").textContent = meta.path;
    dirty = false; saveBtn.disabled = true; saveBtn.textContent = "Save";
    render();
  }

  // ---- field builders (return HTML; inputs carry data-path into state) ----
  const ro = () => (meta.can_edit ? "" : "disabled");
  function text(path, label, value, opts = {}) {
    return `<label class="field ${opts.wide ? "wide" : ""}"><span>${esc(label)}</span>
      <input data-path="${path}" value="${esc(value)}" placeholder="${esc(opts.ph || "")}" ${opts.type ? `type="${opts.type}"` : ""} ${ro()}></label>`;
  }
  function check(path, label, value) {
    return `<label class="field check"><input type="checkbox" data-path="${path}" ${value ? "checked" : ""} ${ro()}><span>${esc(label)}</span></label>`;
  }
  function select(path, label, value, options, opts = {}) {
    const o = options.map((v) => `<option value="${esc(v)}" ${v === value ? "selected" : ""}>${esc(v || opts.empty || "—")}</option>`).join("");
    return `<label class="field"><span>${esc(label)}</span><select data-path="${path}" ${opts.restructure ? "data-restructure" : ""} ${ro()}>${o}</select></label>`;
  }
  function secret(path, label, value) {
    const isRef = /^\$\{[A-Za-z_][A-Za-z0-9_]*\}$/.test(value || "");
    const set = value === MASKED;
    const ph = set ? "set — leave empty to keep" : "value or ${ENV_NAME}";
    return `<label class="field"><span>${esc(label)} ${isRef ? '<em class="muted">from .env</em>' : set ? '<em class="muted">set</em>' : ""}</span>
      <input data-path="${path}" data-secret type="${isRef ? "text" : "password"}" autocomplete="new-password"
        value="${isRef ? esc(value) : ""}" placeholder="${esc(ph)}" ${ro()}></label>`;
  }
  const del = (kind, i) => meta.can_edit ? `<button class="btn small danger" data-del="${kind}" data-i="${i}" type="button">Delete</button>` : "";
  const add = (kind, label) => meta.can_edit ? `<button class="btn small" data-add="${kind}" type="button">+ ${label}</button>` : "";

  function section(title, kind, addLabel, items, hint) {
    return `<section class="set-section"><div class="set-head"><h2>${title}</h2>${add(kind, addLabel)}</div>
      ${hint ? `<p class="muted small">${hint}</p>` : ""}
      <div class="items">${items || '<p class="muted">None yet.</p>'}</div></section>`;
  }

  function renderNetworks() {
    const routerNames = state.routers.map((r) => r.name);
    const snmpNames = state.snmp_targets.map((t) => t.name);
    return state.networks.map((n, i) => {
      const sources = n.sources.map((s, j) => {
        const p = `networks.${i}.sources.${j}`;
        const refs = s.type === "router" ? routerNames : s.type === "snmp" ? snmpNames : [];
        return `<div class="source-row">
          ${select(`${p}.type`, "Source", s.type, ["router", "snmp", "nmap"], { restructure: true })}
          ${s.type === "nmap" ? text(`${p}.args`, "Extra nmap args", (s.args || []).join(" "), { ph: "-T4" })
            : select(`${p}.ref`, s.type === "router" ? "Router" : "SNMP target", s.ref || "", ["", ...refs], { empty: "choose…" })}
          ${text(`${p}.interval`, "Every", s.interval, { ph: "5m" })}
          ${s.type === "nmap" ? check(`${p}.traceroute`, "Traceroute", s.traceroute) : ""}
          ${meta.can_edit ? `<button class="btn small danger" data-del-source="${i}" data-j="${j}" type="button" aria-label="Remove source">×</button>` : ""}
        </div>`;
      }).join("");
      return `<article class="item"><div class="item-head"><b>${esc(n.name || "new network")}</b>${del("networks", i)}</div>
        <div class="grid">
          ${text(`networks.${i}.name`, "Name", n.name)}
          ${text(`networks.${i}.cidr`, "CIDR", n.cidr, { ph: "192.168.1.0/24" })}
          ${text(`networks.${i}.exclude`, "Exclude IPs", n.exclude.join(", "), { ph: "comma separated" })}
          ${text(`networks.${i}.offline_after`, "Offline after", n.offline_after, { ph: "auto (3 missed scans)" })}
        </div>
        <div class="sources"><span class="sub">Discovery sources</span>${sources}
          ${meta.can_edit ? `<button class="btn small" data-add-source="${i}" type="button">+ Source</button>` : ""}</div>
      </article>`;
    }).join("");
  }

  function renderRouters() {
    return state.routers.map((r, i) => `<article class="item"><div class="item-head"><b>${esc(r.name || "new router")}</b> <span class="muted small">MikroTik RouterOS API</span>${del("routers", i)}</div>
      <div class="grid">
        ${text(`routers.${i}.name`, "Name", r.name)}
        ${text(`routers.${i}.address`, "Address", r.address, { ph: "192.168.88.1:8729" })}
        ${text(`routers.${i}.username`, "Username", r.username, { ph: "netmon-ro" })}
        ${secret(`routers.${i}.password`, "Password", r.password)}
        ${check(`routers.${i}.tls`, "TLS (api-ssl)", r.tls)}
        ${check(`routers.${i}.insecure_skip_verify`, "Accept self-signed certificate", r.insecure_skip_verify)}
      </div></article>`).join("");
  }

  function renderSNMP() {
    return state.snmp_targets.map((t, i) => {
      const v3 = t.version === "v3";
      return `<article class="item"><div class="item-head"><b>${esc(t.name || "new target")}</b>${del("snmp_targets", i)}</div>
      <div class="grid">
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
      </div></article>`;
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
      const test = meta.can_edit && c.original_name ? `<button class="btn small" data-test="${esc(c.original_name)}" type="button">Send test</button>` : "";
      return `<article class="item"><div class="item-head"><b>${esc(c.name || "new channel")}</b>${test}${del("channels", i)}</div>
        <div class="grid">${text(`${p}.name`, "Name", c.name)}${select(`${p}.type`, "Type", c.type, ["telegram", "discord", "email"], { restructure: true })}${fields}</div></article>`;
    }).join("");
  }

  function checks(path, all, chosen, emptyMeansAll) {
    const set = new Set(chosen || []);
    return `<div class="checks">${all.map((v) => `<label class="check"><input type="checkbox" data-list="${path}" value="${esc(v)}" ${set.has(v) ? "checked" : ""} ${ro()}>${esc(v)}</label>`).join("")}
      ${emptyMeansAll ? `<span class="muted small">${set.size ? "" : "(none ticked = all)"}</span>` : ""}</div>`;
  }

  function renderRules() {
    return state.rules.map((r, i) => `<article class="item"><div class="item-head"><b>Rule ${i + 1}</b>${del("rules", i)}</div>
      <div class="rule"><span class="sub">Events</span>${checks(`rules.${i}.events`, meta.event_types, r.events, true)}
      <span class="sub">Networks</span>${checks(`rules.${i}.networks`, state.networks.map((n) => n.name), r.networks, true)}
      <span class="sub">Send to</span>${checks(`rules.${i}.channels`, state.channels.map((c) => c.name), r.channels, false)}
      ${check(`rules.${i}.only_unknown`, "Only devices not marked as known", r.only_unknown)}</div></article>`).join("");
  }

  function render() {
    root.innerHTML =
      section("Networks", "networks", "Network", renderNetworks(), "Each network is scanned by its sources; hosts outside the CIDR are ignored.") +
      section("Routers", "routers", "Router", renderRouters(), "Use a read-only RouterOS API user. Secrets can be written as ${NAME} to keep them in .env.") +
      section("SNMP targets", "snmp_targets", "SNMP target", renderSNMP()) +
      section("Notification channels", "channels", "Channel", renderChannels(), "Save before sending a test to a new or changed channel.") +
      section("Notification rules", "rules", "Rule", renderRules(), "Changes from one scan are batched into one message per channel.");
  }

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
  const numFields = /\.(port|smtp_port)$/;
  // Re-render after focus has moved (e.g. Tab), then put focus back on the same field.
  function renderKeepingFocus() {
    setTimeout(() => {
      const path = document.activeElement?.dataset?.path;
      render();
      if (path) root.querySelector(`[data-path="${CSS.escape(path)}"]`)?.focus();
    });
  }

  root.addEventListener("change", (e) => {
    const el = e.target;
    if (el.dataset.list) {
      const set = new Set(getPath(el.dataset.list) || []);
      el.checked ? set.add(el.value) : set.delete(el.value);
      setPath(el.dataset.list, [...set]);
      markDirty(); render(); return;
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
    }
    setPath(path, v);
    markDirty();
    if (el.dataset.restructure !== undefined || path.endsWith(".name")) renderKeepingFocus();
  });

  const blank = {
    networks: () => ({ name: "", cidr: "", sources: [{ type: "nmap", interval: "15m" }], exclude: [] }),
    routers: () => ({ name: "", type: "mikrotik", address: "", username: "netmon-ro", password: "", tls: true, insecure_skip_verify: true }),
    snmp_targets: () => ({ name: "", address: "", version: "v2c", community: "" }),
    channels: () => ({ name: "", type: "telegram" }),
    rules: () => ({ events: ["device_new"], channels: [], only_unknown: true }),
  };
  root.addEventListener("click", (e) => {
    const t = e.target;
    if (t.dataset.add) { state[t.dataset.add].push(blank[t.dataset.add]()); markDirty(); render(); }
    if (t.dataset.del) {
      const item = state[t.dataset.del][+t.dataset.i];
      if (!confirm(`Delete ${item.name || "this item"}?`)) return;
      state[t.dataset.del].splice(+t.dataset.i, 1); markDirty(); render();
    }
    if (t.dataset.addSource) { state.networks[+t.dataset.addSource].sources.push({ type: "nmap", interval: "15m" }); markDirty(); render(); }
    if (t.dataset.delSource) { state.networks[+t.dataset.delSource].sources.splice(+t.dataset.j, 1); markDirty(); render(); }
  });

  function showErrors(list) {
    errorsEl.hidden = !list.length;
    errorsEl.innerHTML = list.length ? `<b>Not saved:</b><ul>${list.map((l) => `<li>${esc(l)}</li>`).join("")}</ul>` : "";
    if (list.length) errorsEl.scrollIntoView({ behavior: "smooth", block: "start" });
  }

  saveBtn.addEventListener("click", async () => {
    saveBtn.disabled = true; saveBtn.textContent = "Saving…";
    const res = await fetch(url("/api/settings"), { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(state) });
    if (res.status === 204) { showErrors([]); toast("Saved and applied"); dirty = false; return load(); }
    let errs = [res.statusText];
    try { errs = (await res.json()).errors; } catch { errs = [await res.text().catch(() => res.statusText)]; }
    showErrors(errs); saveBtn.disabled = false; saveBtn.textContent = "Save changes";
  });

  document.getElementById("reload").addEventListener("click", async () => {
    if (dirty && !confirm("Discard unsaved changes and reload from the file?")) return;
    const res = await fetch(url("/api/settings/reload"), { method: "POST" });
    if (res.status === 204) { showErrors([]); toast("Reloaded from file"); dirty = false; return load(); }
    let errs; try { errs = (await res.json()).errors; } catch { errs = [res.statusText]; }
    showErrors(errs);
  });

  load();
})();
