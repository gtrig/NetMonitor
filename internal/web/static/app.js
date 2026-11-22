// Shared helpers and page behaviour for netmon.
const toastEl = document.getElementById("toast");
let toastTimer;
function toast(msg) {
  toastEl.textContent = msg;
  toastEl.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => toastEl.classList.remove("show"), 2500);
}

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  return res.status === 204 || res.status === 202 ? null : res.json();
}

async function patchDevice(key, fields) {
  await api("PATCH", "/api/devices/" + encodeURIComponent(key), fields);
}

document.addEventListener("click", async (e) => {
  const scan = e.target.closest("[data-scan]");
  if (scan) {
    try { await api("POST", "/api/scan/" + encodeURIComponent(scan.dataset.scan)); toast("Scan started for " + scan.dataset.scan); }
    catch (err) { toast("Scan failed: " + err.message); }
  }
  const test = e.target.closest("[data-test]");
  if (test) {
    test.disabled = true;
    try { await api("POST", "/api/notify/test/" + encodeURIComponent(test.dataset.test)); toast("Test sent to " + test.dataset.test); }
    catch (err) { toast("Test failed: " + err.message); }
    finally { test.disabled = false; }
  }
});

// Devices page: inline label editing, known toggle, client-side filter.
const table = document.getElementById("devices");
if (table) {
  table.addEventListener("change", async (e) => {
    const row = e.target.closest("tr[data-key]");
    if (!row) return;
    try {
      if (e.target.classList.contains("known")) {
        await patchDevice(row.dataset.key, { known: e.target.checked });
        row.dataset.known = String(e.target.checked);
        toast(e.target.checked ? "Marked as known" : "Marked as unreviewed");
      } else if (e.target.classList.contains("label")) {
        await patchDevice(row.dataset.key, { label: e.target.value.trim() });
        toast("Label saved");
      }
    } catch (err) { toast("Save failed: " + err.message); }
  });
  const q = document.getElementById("q");
  const only = document.getElementById("onlyUnknown");
  const apply = () => {
    const term = q.value.trim().toLowerCase();
    for (const row of table.tBodies[0].rows) {
      if (!row.dataset.key) continue;
      const text = (row.textContent + " " + row.querySelector("input.label").value).toLowerCase();
      const show = (!term || text.includes(term)) && (!only.checked || row.dataset.known === "false");
      row.hidden = !show;
    }
  };
  q.addEventListener("input", apply);
  only.addEventListener("change", apply);
}
