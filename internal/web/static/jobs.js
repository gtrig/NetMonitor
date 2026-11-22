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
  // Server times are compared against the local clock, corrected by the poll age.
  const since = (t) => (Date.now() - new Date(t)) / 1000;

  function row(j) {
    const state = j.state || "idle";
    let progress = "";
    if (state === "running") {
      const elapsed = dur(since(j.startedAt));
      progress = j.progress != null
        ? `<div class="bar-outer" role="progressbar" aria-valuenow="${Math.round(j.progress)}" aria-valuemin="0" aria-valuemax="100"><div class="bar-inner" style="width:${j.progress}%"></div></div>
           <span class="small">${esc(j.task)} ${j.progress.toFixed(0)}% · ${elapsed}</span>`
        : `<span class="spinner" aria-hidden="true"></span> <span class="small">${elapsed}</span>`;
    } else if (state === "queued") {
      progress = `<span class="muted small">waiting for a free slot</span>`;
    }
    const next = state === "idle" && !zero(j.nextRun) ? "in " + dur(-since(j.nextRun)) : state === "idle" ? "" : "—";
    const last = zero(j.lastRun) ? `<span class="muted">never</span>`
      : `${j.lastErr ? '<span class="pill bad"></span>' : '<span class="pill ok"></span>'} ${dur(since(j.lastRun))} ago` +
        (j.lastDuration ? ` <span class="muted small">took ${dur(j.lastDuration)}</span>` : "") +
        (j.lastErr ? `<div class="err">${esc(j.lastErr)}</div>` : "");
    return `<tr>
      <td>${esc(j.network)}</td><td class="mono">${esc(j.source)} <span class="muted small">every ${esc(j.interval)}</span></td>
      <td><span class="state state-${esc(state)}">${esc(state)}</span></td>
      <td class="progress-cell">${progress}</td><td>${next}</td><td>${last}</td>
      <td><button class="btn small" data-job="${j.id}" ${state === "idle" ? "" : "disabled"}>Scan now</button></td></tr>`;
  }

  function render() {
    body.innerHTML = jobs.length ? jobs.map(row).join("") : `<tr><td colspan="7" class="muted">No scan jobs configured.</td></tr>`;
  }

  async function poll() {
    try { jobs = await api("GET", "/api/jobs"); fetchedAt = Date.now(); render(); } catch { /* keep last state */ }
  }

  body.addEventListener("click", async (e) => {
    const b = e.target.closest("[data-job]");
    if (!b) return;
    b.disabled = true;
    try { await api("POST", `/api/jobs/${b.dataset.job}/scan`); toast("Scan started"); setTimeout(poll, 300); }
    catch (err) { toast("Scan failed: " + err.message); b.disabled = false; }
  });

  poll();
  setInterval(poll, 2000);
  setInterval(() => { if (Date.now() - fetchedAt > 900) render(); }, 1000);
})();
