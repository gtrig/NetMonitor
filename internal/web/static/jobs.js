// Jobs page: live scan queue and job tables (polls /api/jobs, ticks timers every second).
(() => {
  const body = document.querySelector("#jobs tbody");
  if (!body) return;
  const queueBody = document.querySelector("#queue tbody");
  const queueCount = document.getElementById("queue-count");
  let jobs = [];
  let fetchedAt = Date.now();
  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const zero = (t) => !t || t.startsWith("0001");
  const dur = (sec) => {
    sec = Math.max(0, Math.round(sec));
    if (sec < 60) return sec + "s";
    const m = Math.floor(sec / 60), s = sec % 60;
    if (m < 60) return m + "m " + String(s).padStart(2, "0") + "s";
    return Math.floor(m / 60) + "h " + String(m % 60).padStart(2, "0") + "m";
  };
  // Server timestamps are compared with the browser clock (same host or NTP-synced LAN).
  const since = (t) => (Date.now() - new Date(t)) / 1000;

  function progressOf(j) {
    let progress = "";
    if (j.state === "running") {
      const elapsed = dur(since(j.startedAt));
      progress = j.progress != null
        ? `<div class="mb-0.5 h-1.5 overflow-hidden rounded-[3px] border bg-page" role="progressbar" aria-valuenow="${Math.round(j.progress)}" aria-valuemin="0" aria-valuemax="100"><div class="h-full bg-accent transition-[width] duration-500 motion-reduce:transition-none" style="width:${j.progress}%"></div></div>
           <span class="text-xs">${esc(j.task)} ${j.progress.toFixed(0)}% · ${elapsed}</span>`
        : `<span class="inline-block size-2.5 animate-spin rounded-full border-2 border-t-accent align-[-1px] [animation-duration:.8s] motion-reduce:animate-none" aria-hidden="true"></span> <span class="text-xs">${j.task ? esc(j.task) + " · " : ""}${elapsed}</span>`;
    } else if (j.state === "queued") {
      progress = `<span class="text-muted text-xs">${j.position ? `number ${j.position} in the queue` : "starting"}</span>`;
    }
    return progress;
  }

  // The running and waiting scans. Pings and detailed jobs (which wait for their devices) use no worker.
  const inQueue = (j) => j.priority && j.state !== "idle";
  function queueRow(j) {
    const waiting = j.position > 0;
    return `<tr><td class="tabular-nums">${waiting ? j.position : `<span class="state" data-state="running">running</span>`}</td>
      <td>${esc(j.network)}</td><td class="mono">${esc(j.source)}</td><td>${esc(j.priority)}</td>
      <td class="min-w-[180px]">${waiting ? `<span class="text-muted text-xs">waiting</span>` : progressOf(j)}</td>
      <td>${waiting ? `<button class="btn btn-sm" type="button" data-cancel="${esc(j.queueId)}" aria-label="Cancel ${esc(j.source)} in ${esc(j.network)}">Cancel</button>` : ""}</td></tr>`;
  }

  function row(j) {
    const state = j.state || "idle";
    const progress = progressOf(j);
    const next = state === "idle" && !zero(j.nextRun) ? "in " + dur(-since(j.nextRun)) : state === "idle" ? "" : "—";
    const last = zero(j.lastRun) ? `<span class="text-muted">never</span>`
      : `${j.lastErr ? '<span class="pill bg-bad"></span>' : '<span class="pill bg-ok"></span>'} ${dur(since(j.lastRun))} ago` +
        (j.lastDuration ? ` <span class="text-muted text-xs">took ${dur(j.lastDuration)}</span>` : "") +
        (j.lastErr ? `<div class="ml-4.5 text-xs wrap-break-word text-bad">${esc(j.lastErr)}</div>` : "");
    return `<tr>
      <td>${esc(j.network)}</td><td class="mono">${esc(j.source)} <span class="text-muted text-xs">every ${esc(j.interval)}</span></td>
      <td><span class="state" data-state="${esc(state)}">${esc(state)}</span></td>
      <td class="min-w-[180px]">${progress}</td><td>${next}</td><td>${last}</td>
      <td><button class="btn btn-sm" data-job="${j.id}" ${state === "idle" ? "" : "disabled"}>Scan now</button></td></tr>`;
  }

  function render() {
    const focused = document.activeElement?.dataset?.cancel;
    const queued = jobs.filter(inQueue).sort((a, b) => (a.position || 0) - (b.position || 0));
    const waiting = queued.filter((j) => j.position).length;
    queueCount.textContent = queued.length ? `${queued.length - waiting} running, ${waiting} waiting` : "";
    queueBody.innerHTML = queued.length ? queued.map(queueRow).join("") : `<tr><td colspan="6" class="text-muted">Nothing is running or waiting.</td></tr>`;
    if (focused) queueBody.querySelector(`[data-cancel="${CSS.escape(focused)}"]`)?.focus({ preventScroll: true });
    const rows = jobs.filter((j) => j.id >= 0);
    body.innerHTML = rows.length ? rows.map(row).join("") : `<tr><td colspan="7" class="text-muted">No scan jobs configured.</td></tr>`;
  }

  // Reload the first history page whenever a job finished.
  const history = document.getElementById("history");
  let seen = null;
  async function refreshHistory() {
    const now = jobs.map((j) => j.source === "ping" ? [j.queueId, j.lastErr] : [j.queueId, j.id < 0 ? "" : j.lastRun, j.running]).join();
    const changed = seen !== null && now !== seen;
    seen = now;
    if (!changed || "older" in history.dataset || history.contains(document.activeElement) || !getSelection().isCollapsed) return;
    const res = await fetch(location.href);
    if (!res.ok) return;
    const fresh = new DOMParser().parseFromString(await res.text(), "text/html").getElementById("history");
    if (fresh) history.innerHTML = fresh.innerHTML;
  }

  async function poll() {
    if (document.hidden) return;
    try { jobs = await api("GET", "/api/jobs"); fetchedAt = Date.now(); render(); refreshHistory(); } catch { /* keep last state */ }
  }

  body.addEventListener("click", async (e) => {
    const b = e.target.closest("[data-job]");
    if (!b) return;
    b.disabled = true;
    const j = jobs.find((x) => String(x.id) === b.dataset.job) || {};
    try { await api("POST", `/api/jobs/${b.dataset.job}/scan?network=${encodeURIComponent(j.network || "")}&source=${encodeURIComponent(j.source || "")}`); toast("Scan started"); setTimeout(poll, 300); }
    catch (err) { toast("Scan failed: " + err.message); b.disabled = false; }
  });

  queueBody.addEventListener("click", async (e) => {
    const b = e.target.closest("[data-cancel]");
    if (!b) return;
    b.disabled = true;
    try { await api("POST", "/api/queue/cancel", { id: b.dataset.cancel }); toast("Scan cancelled"); }
    catch (err) { toast("Not cancelled: " + err.message); }
    poll();
  });

  poll();
  setInterval(poll, 2000);
  document.addEventListener("visibilitychange", () => { if (!document.hidden) poll(); });
  setInterval(() => { if (Date.now() - fetchedAt > 900) render(); }, 1000);
})();
