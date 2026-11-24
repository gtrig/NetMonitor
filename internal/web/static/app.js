// Shared helpers and page behaviour for netmon.
const toastEl = document.getElementById("toast");
let toastTimer;
function toast(msg) {
  toastEl.textContent = msg;
  toastEl.dataset.show = "";
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => delete toastEl.dataset.show, 2500);
}

// Absolute same-origin URL: fetch() rejects relative URLs when the page URL carries
// credentials (http://user:pass@host/...).
const url = (path) => new URL(path, location.origin).href;

async function api(method, path, body) {
  const res = await fetch(url(path), {
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

// Device details page.
const devicePage = document.getElementById("device");
if (devicePage) {
  const ref = { network: devicePage.dataset.network, key: devicePage.dataset.key };
  const scannedAt = async () => (await api("GET", "/api/details?network=" + encodeURIComponent(ref.network)))
    .find((d) => d.key === ref.key)?.scannedAt;
  devicePage.addEventListener("change", async (e) => {
    try {
      if (e.target.classList.contains("known")) {
        await patchDevice(ref.key, { known: e.target.checked });
        toast(e.target.checked ? "Marked as known" : "Marked as unreviewed");
      } else if (e.target.classList.contains("label")) {
        await patchDevice(ref.key, { label: e.target.value.trim() });
        toast("Label saved");
      }
    } catch (err) { toast("Save failed: " + err.message); }
  });
  devicePage.addEventListener("click", async (e) => {
    const b = e.target.closest("button");
    if (!b) return;
    try {
      if (b.classList.contains("scan-details")) {
        const before = await scannedAt();
        await api("POST", "/api/details/scan", ref);
        b.disabled = true;
        b.textContent = "Queued…";
        toast("Detailed scan queued; this page reloads when it is done");
        const until = Date.now() + 15 * 60000;
        const poll = setInterval(async () => {
          try {
            if (await scannedAt() !== before) { clearInterval(poll); location.reload(); return; }
          } catch { /* try again */ }
          if (Date.now() > until) { clearInterval(poll); b.disabled = false; b.textContent = "Scan again"; toast("No result yet; check the Jobs page"); }
        }, 4000);
      } else if (b.classList.contains("wake")) {
        await api("POST", "/api/devices/wake", ref);
        toast("Wake packet sent");
      } else if (b.classList.contains("split-off")) {
        await api("POST", "/api/machines/split", ref);
        location.reload();
      } else if (b.classList.contains("forget")) {
        if (!confirm(`Forget ${ref.key} in ${ref.network}? It comes back if a scan sees it again.`)) return;
        await api("DELETE", "/api/devices/" + encodeURIComponent(ref.key) + "?network=" + encodeURIComponent(ref.network));
        location.href = "/devices";
      }
    } catch (err) { toast("Failed: " + err.message); }
  });
}
