// Devices page: sorting, column choice, filter, inline editing and actions on one or many devices.
(() => {
  const table = document.getElementById("devices");
  const body = table.tBodies[0];
  const heads = [...table.tHead.rows[0].cells];
  const rows = () => [...body.rows].filter((r) => r.dataset.key);
  const refOf = (row) => ({ network: row.dataset.network, key: row.dataset.key });
  // window.localStorage itself throws when storage is blocked.
  const storage = { getItem: (k) => localStorage.getItem(k), setItem: (k, v) => localStorage.setItem(k, v) };
  const prefs = DevicesTable.load(storage);

  // Columns.
  const applyColumns = () => {
    heads.forEach((th, i) => {
      const hide = "optional" in th.dataset && prefs.hidden.includes(th.dataset.col);
      // The actions header holds the column menu, so only the buttons below it are hidden.
      if (th.dataset.col === "actions") {
        for (const r of rows()) r.cells[i].firstElementChild.hidden = hide;
        return;
      }
      th.hidden = hide;
      for (const r of rows()) r.cells[i].hidden = hide;
    });
  };
  const menu = document.getElementById("columns");
  for (const th of heads) {
    if (!("optional" in th.dataset)) continue;
    const label = document.createElement("label");
    label.className = "menu-item";
    const box = document.createElement("input");
    box.type = "checkbox";
    box.autocomplete = "off";
    box.checked = !prefs.hidden.includes(th.dataset.col);
    box.addEventListener("change", () => {
      prefs.hidden = prefs.hidden.filter((c) => c !== th.dataset.col);
      if (!box.checked) prefs.hidden.push(th.dataset.col);
      DevicesTable.save(storage, prefs);
      applyColumns();
    });
    label.append(box, th.dataset.label);
    menu.append(label);
  }
  // A popover, because the table's scroll area would clip it; closing is up to the browser.
  menu.addEventListener("toggle", (e) => {
    if (e.newState !== "open") return;
    const at = document.getElementById("columnsBtn").getBoundingClientRect();
    menu.style.top = at.bottom + 4 + "px";
    menu.style.right = Math.max(8, document.documentElement.clientWidth - at.right) + "px";
  });

  // Sorting.
  const original = rows();
  const valueOf = (row, i, col) => {
    if (col === "name") { const l = row.querySelector("input.label"); return l.value.trim() || l.dataset.hostname; }
    if (col === "known") return row.dataset.known === "true" ? "0" : "1";
    return row.cells[i].dataset.sort ?? row.cells[i].textContent.trim();
  };
  const applySort = () => {
    const i = heads.findIndex((th) => th.dataset.col === prefs.sort?.col && th.dataset.type);
    heads.forEach((th, j) => {
      if (!th.dataset.type) return;
      th.setAttribute("aria-sort", i === j ? (prefs.sort.dir === "asc" ? "ascending" : "descending") : "none");
    });
    let list = original.filter((r) => r.isConnected);
    if (i >= 0) {
      const values = list.map((r) => valueOf(r, i, prefs.sort.col));
      list = DevicesTable.order(values, heads[i].dataset.type, prefs.sort.dir).map((k) => list[k]);
    }
    body.append(...list);
  };
  table.tHead.addEventListener("click", (e) => {
    const th = e.target.closest("button.sort")?.closest("th");
    if (!th) return;
    prefs.sort = DevicesTable.nextSort(prefs.sort, th.dataset.col);
    DevicesTable.save(storage, prefs);
    applySort();
  });

  // Selection. Only listed rows can be selected, so actions never touch filtered-out devices.
  const all = document.getElementById("selAll");
  const bar = document.getElementById("bulk");
  const boxOf = (row) => row.querySelector("input.sel");
  const selected = () => rows().filter((r) => boxOf(r).checked);
  let busy = false;
  const syncSelection = () => {
    const listed = rows().filter((r) => !r.hidden);
    const sel = selected();
    all.checked = sel.length > 0 && sel.length === listed.length;
    all.indeterminate = sel.length > 0 && sel.length < listed.length;
    all.disabled = !listed.length;
    for (const r of rows()) r.toggleAttribute("data-selected", boxOf(r).checked);
    bar.hidden = !sel.length;
    document.getElementById("bulkCount").textContent = `${sel.length} selected`;
    for (const b of bar.querySelectorAll("button")) {
      b.disabled = busy || (b.dataset.bulk === "merge" && sel.length < 2) ||
        (b.dataset.bulk === "wake" && !sel.some((r) => r.querySelector(".wake"))) ||
        (b.dataset.bulk === "split" && !sel.some((r) => r.dataset.machine));
    }
  };
  all.addEventListener("change", () => {
    for (const r of rows()) boxOf(r).checked = all.checked && !r.hidden;
    syncSelection();
  });

  // Filter.
  const q = document.getElementById("q");
  const only = document.getElementById("onlyUnknown");
  const searchable = heads.flatMap((th, i) => (["select", "status", "known", "actions"].includes(th.dataset.col) ? [] : [i]));
  const applyFilter = () => {
    const term = q.value.trim().toLowerCase();
    for (const row of rows()) {
      const label = row.querySelector("input.label");
      const text = [label.value, label.dataset.hostname, ...searchable.map((i) => row.cells[i].textContent)].join(" ").toLowerCase();
      row.hidden = !((!term || text.includes(term)) && (!only.checked || row.dataset.known === "false"));
      if (row.hidden) boxOf(row).checked = false;
    }
    syncSelection();
  };
  q.addEventListener("input", applyFilter);
  only.addEventListener("change", applyFilter);

  // Actions on one device.
  const setKnown = async (row, known) => {
    await patchDevice(row.dataset.key, { known });
    // The flag belongs to the key, which may be listed in several networks.
    for (const r of rows()) {
      if (r.dataset.key !== row.dataset.key) continue;
      r.dataset.known = String(known);
      r.querySelector("input.known").checked = known;
    }
  };
  const scan = async (row) => {
    await api("POST", "/api/details/scan", refOf(row));
    const b = row.querySelector(".scan-details");
    b.disabled = true;
    b.title = "Queued for a detailed scan";
  };
  const wake = (row) => api("POST", "/api/devices/wake", refOf(row));
  const split = (row) => api("POST", "/api/machines/split", refOf(row));
  const forget = async (row) => {
    const { network, key } = refOf(row);
    await api("DELETE", "/api/devices/" + encodeURIComponent(key) + "?network=" + encodeURIComponent(network));
    row.remove();
  };

  table.addEventListener("change", async (e) => {
    const row = e.target.closest("tr[data-key]");
    if (!row) return;
    const c = e.target.classList;
    if (c.contains("sel")) { syncSelection(); return; }
    try {
      if (c.contains("known")) {
        const known = e.target.checked;
        await setKnown(row, known);
        toast(known ? "Marked as known" : "Marked as unreviewed");
      } else if (c.contains("label")) {
        await patchDevice(row.dataset.key, { label: e.target.value.trim() });
        toast("Label saved");
      }
    } catch (err) {
      if (c.contains("known")) e.target.checked = row.dataset.known === "true";
      toast("Save failed: " + err.message);
    }
  });
  table.addEventListener("click", async (e) => {
    const row = e.target.closest("tr[data-key]");
    const c = e.target.classList;
    if (!row) return;
    try {
      if (c.contains("scan-details")) {
        await scan(row);
        toast("Detailed scan queued; see the Jobs page for progress. Reload when it is done.");
      } else if (c.contains("wake")) {
        const { via } = await wake(row);
        toast("Wake packet sent via " + via);
      } else if (c.contains("split")) {
        await split(row);
        location.reload();
      } else if (c.contains("forget")) {
        if (!confirm(`Forget ${row.dataset.key} in ${row.dataset.network}? It comes back if a scan sees it again.`)) return;
        await forget(row);
        if (row.dataset.machine) { location.reload(); return; } // its machine may have dissolved
        syncSelection();
        toast("Device forgotten");
      }
    } catch (err) { toast("Failed: " + err.message); }
  });

  // Actions on the selected devices.
  const each = async (list, fn, done) => {
    const failed = [];
    let lastErr;
    for (const [i, row] of list.entries()) {
      document.getElementById("bulkCount").textContent = `${i + 1} of ${list.length}…`;
      try { await fn(row); boxOf(row).checked = false; } catch (err) { failed.push(row); lastErr = err; }
    }
    const ok = list.length - failed.length;
    toast(failed.length ? `${done(ok)}; ${failed.length} failed: ${lastErr.message}` : done(ok));
    return failed.length === 0;
  };
  const devices = (n) => `${n} device${n === 1 ? "" : "s"}`;
  const bulk = {
    known: (l) => each(l, (r) => setKnown(r, true), (n) => `${devices(n)} marked as known`),
    unknown: (l) => each(l, (r) => setKnown(r, false), (n) => `${devices(n)} marked as unreviewed`),
    scan: (l) => each(l, scan, (n) => `${devices(n)} queued for a detailed scan`),
    wake: (l) => each(l.filter((r) => r.querySelector(".wake")), wake, (n) => `Wake packet sent to ${devices(n)}`),
    async merge(l) { await api("POST", "/api/machines", { members: l.map(refOf) }); location.reload(); },
    async split(l) {
      if (await each(l.filter((r) => r.dataset.machine), split, (n) => `${devices(n)} split`)) location.reload();
    },
    async forget(l) {
      if (!confirm(`Forget ${devices(l.length)}? They come back if a scan sees them again.`)) return;
      const ok = await each(l, forget, (n) => `${devices(n)} forgotten`);
      if (ok && l.some((r) => r.dataset.machine)) location.reload();
    },
  };
  bar.addEventListener("click", async (e) => {
    const b = e.target.closest("button");
    if (!b || busy) return;
    if (b.dataset.bulk === "clear") {
      for (const r of selected()) boxOf(r).checked = false;
      syncSelection();
      return;
    }
    const list = selected();
    busy = true;
    syncSelection();
    try { await bulk[b.dataset.bulk](list); } catch (err) { toast("Failed: " + err.message); }
    busy = false;
    syncSelection();
  });

  // Clicking a row opens the device's page; ctrl/cmd/middle click opens it in a new tab.
  const openRow = (e) => {
    const row = e.target.closest("tr[data-key]");
    if (!row || e.target.closest("a, button, input, label, details, .select-cell") || String(getSelection())) return;
    const href = row.querySelector(".device-link").href;
    if (e.ctrlKey || e.metaKey || e.button === 1) window.open(href, "_blank", "noopener");
    else if (e.button === 0) location.href = href;
  };
  table.addEventListener("click", openRow);
  table.addEventListener("auxclick", (e) => { if (e.button === 1) openRow(e); });

  applyColumns();
  applySort();
  q.value = new URLSearchParams(location.search).get("q") || q.value;
  applyFilter();
})();
