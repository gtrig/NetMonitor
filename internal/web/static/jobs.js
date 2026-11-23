// Overview page: live scan job table (polls /api/jobs, ticks timers every second).
(() => {
  const body = document.querySelector("#jobs tbody");
  if (!body) return;
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

  function row(j) {
    const state = j.state || "idle";
    let progress = "";
    if (state === "running") {
      const elapsed = dur(since(j.startedAt));
      progress = j.progress != null
        ? `<div class="mb-0.5 h-1.5 overflow-hidden rounded-[3px] border bg-page" role="progressbar" aria-valuenow="${Math.round(j.progress)}" aria-valuemin="0" aria-valuemax="100"><div class="h-full bg-accent transition-[width] duration-500 motion-reduce:transition-none" style="width:${j.progress}%"></div></div>
           <span class="text-xs">${esc(j.task)} ${j.progress.toFixed(0)}% · ${elapsed}</span>`
        : `<span class="inline-block size-2.5 animate-spin rounded-full border-2 border-t-accent align-[-1px] [animation-duration:.8s] motion-reduce:animate-none" aria-hidden="true"></span> <span class="text-xs">${j.task ? esc(j.task) + " · " : ""}${elapsed}</span>`;
    } else if (state === "queued") {
      progress = `<span class="text-muted text-xs">waiting for a free slot</span>`;
    }
    const next = state === "idle" && !zero(j.nextRun) ? "in " + dur(-since(j.nextRun)) : state === "idle" ? "" : "—";
    const last = zero(j.lastRun) ? `<span class="text-muted">never</span>`
      : `${j.lastErr ? '<span class="pill bg-bad"></span>' : '<span class="pill bg-ok"></span>'} ${dur(since(j.lastRun))} ago` +
        (j.lastDuration ? ` <span class="text-muted text-xs">took ${dur(j.lastDuration)}</span>` : "") +
        (j.lastErr ? `<div class="ml-4.5 text-xs wrap-break-word text-bad">${esc(j.lastErr)}</div>` : "");
    return `<tr>
      <td>${esc(j.network)}</td><td class="mono">${esc(j.source)} <span class="text-muted text-xs">every ${esc(j.interval)}</span></td>
      <td><span class="state" data-state="${esc(state)}">${esc(state)}</span></td>
      <td class="min-w-[180px]">${progress}</td><td>${next}</td><td>${last}</td>
      <td>${j.id >= 0 ? `<button class="btn btn-sm" data-job="${j.id}" ${state === "idle" ? "" : "disabled"}>Scan now</button>` : '<span class="text-muted text-xs">on demand</span>'}</td></tr>`;
  }

  function render() {
    body.innerHTML = jobs.length ? jobs.map(row).join("") : `<tr><td colspan="7" class="text-muted">No scan jobs configured.</td></tr>`;
  }

  async function poll() {
    if (document.hidden) return;
    try { jobs = await api("GET", "/api/jobs"); fetchedAt = Date.now(); render(); } catch { /* keep last state */ }
  }

  body.addEventListener("click", async (e) => {
    const b = e.target.closest("[data-job]");
    if (!b) return;
    b.disabled = true;
    const j = jobs.find((x) => String(x.id) === b.dataset.job) || {};
    try { await api("POST", `/api/jobs/${b.dataset.job}/scan?network=${encodeURIComponent(j.network || "")}&source=${encodeURIComponent(j.source || "")}`); toast("Scan started"); setTimeout(poll, 300); }
    catch (err) { toast("Scan failed: " + err.message); b.disabled = false; }
  });

  poll();
  setInterval(poll, 2000);
  document.addEventListener("visibilitychange", () => { if (!document.hidden) poll(); });
  setInterval(() => { if (Date.now() - fetchedAt > 900) render(); }, 1000);
})();
