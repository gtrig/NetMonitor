// Ping history of a device: graph, summary and list. The helpers are pure; mount() needs a page.
(function (root) {
  const fmt = (ms) => (ms >= 9.95 ? Math.round(ms) : ms > 0 && ms < 0.095 ? ms.toFixed(2) : ms.toFixed(1)) + " ms";

  // segments splits the points into stretches to draw as one line: a missed run, a gap much longer
  // than usual or a change of origin ends one.
  function segments(points) {
    const steps = points.slice(1).map((p, i) => p.t - points[i].t).sort((a, b) => a - b);
    const limit = steps.length ? steps[steps.length >> 1] * 2.5 : Infinity;
    const out = [];
    let cur = null, prev = null;
    for (const p of points) {
      if (p.ms == null) { cur = prev = null; continue; }
      if (!cur || p.t - prev.t > limit || p.origin !== prev.origin) out.push((cur = []));
      cur.push(p);
      prev = p;
    }
    return out;
  }

  // scale maps ms to 0..1 with round ticks.
  function scale(values, log) {
    const top = Math.max(0, ...values);
    if (log) {
      const hi = Math.max(1, Math.ceil(Math.log10(Math.max(top, 1)) - 1e-9)), ticks = [];
      for (let e = -1; e <= hi; e++) ticks.push(Number((10 ** e).toPrecision(6)));
      return { max: 10 ** hi, ticks, frac: (v) => Math.min(1, Math.max(0, (Math.log10(Math.max(v, 0.1)) + 1) / (hi + 1))) };
    }
    const want = Math.max(top, 1) / 4, mag = 10 ** Math.floor(Math.log10(want));
    const step = [1, 2, 5, 10].map((m) => m * mag).find((x) => x >= want - 1e-9);
    const max = Math.max(1, Math.ceil(Math.max(top, 1) / step - 1e-9)) * step, ticks = [];
    for (let i = 0; i * step <= max + 1e-9; i++) ticks.push(Number((i * step).toPrecision(6)));
    return { max, ticks, frac: (v) => v / max };
  }

  function nearest(points, t) {
    let best = -1;
    points.forEach((p, i) => { if (best < 0 || Math.abs(p.t - t) < Math.abs(points[best].t - t)) best = i; });
    return best;
  }

  function timeTicks(from, to, n) {
    const H = 3600e3, span = to - from;
    const step = [H / 6, H / 4, H / 2, H, 2 * H, 3 * H, 4 * H, 6 * H, 12 * H, 24 * H, 48 * H, 96 * H, 168 * H].find((s) => span / s <= n) || 168 * H;
    const d = new Date(from);
    d.setHours(0, 0, 0, 0); // local midnight, so ticks fall on whole local hours
    const out = [];
    for (let t = d.getTime(); t <= to; t += step) if (t >= from) out.push({ t, day: step >= 24 * H });
    return out;
  }

  // geometry of a chart: plot box and scales.
  function frame(data, o) {
    const m = o.mini ? { l: 2, r: 2, t: 4, b: 8 } : { l: 52, r: 12, t: 12, b: 34 };
    const w = Math.max(10, o.width - m.l - m.r), h = Math.max(10, o.height - m.t - m.b);
    const sc = scale(data.points.flatMap((p) => [p.ms, p.max]).filter((v) => v != null), o.log);
    const span = Math.max(1, data.to - data.from);
    return { m, w, h, sc, x: (t) => m.l + ((t - data.from) / span) * w, y: (v) => m.t + h - sc.frac(v) * h, t: (px) => data.from + ((px - m.l) / w) * span };
  }

  function svg(data, o) {
    const f = frame(data, o), n = (v) => v.toFixed(1), base = f.m.t + f.h;
    let s = "";
    if (!o.mini) {
      for (const v of f.sc.ticks) s += `<line class="grid" x1="${f.m.l}" x2="${n(f.m.l + f.w)}" y1="${n(f.y(v))}" y2="${n(f.y(v))}"/><text x="${f.m.l - 8}" y="${n(f.y(v) + 4)}" text-anchor="end">${v} ms</text>`;
      for (const k of timeTicks(data.from, data.to, Math.max(2, Math.floor(f.w / 62)))) {
        const d = new Date(k.t);
        const label = k.day || (d.getHours() === 0 && d.getMinutes() === 0) ? d.toLocaleDateString([], { day: "numeric", month: "short" }) : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
        s += `<text x="${n(f.x(k.t))}" y="${n(base + 24)}" text-anchor="middle">${label}</text>`;
      }
    }
    for (const seg of segments(data.points)) {
      if (seg.length > 1 && seg[0].min != null) s += `<path class="band" d="M${seg.map((p) => `${n(f.x(p.t))} ${n(f.y(p.max))}`).join("L")}L${seg.slice().reverse().map((p) => `${n(f.x(p.t))} ${n(f.y(p.min))}`).join("L")}Z"/>`;
      if (seg.length === 1) s += `<circle class="dot" cx="${n(f.x(seg[0].t))}" cy="${n(f.y(seg[0].ms))}" r="${o.mini ? 1.5 : 2.5}"/>`;
      else s += `<path class="line" d="M${seg.map((p) => `${n(f.x(p.t))} ${n(f.y(p.ms))}`).join("L")}"/>`;
    }
    for (const p of data.points) if (p.missed) s += `<line class="miss" x1="${n(f.x(p.t))}" x2="${n(f.x(p.t))}" y1="${n(base)}" y2="${n(base + (o.mini ? 6 : 9))}"/>`;
    return s;
  }

  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const parse = (d) => ({ from: Date.parse(d.from), to: Date.parse(d.to), bucket: d.bucket, summary: d.summary, points: d.points.map((p) => ({ ...p, t: Date.parse(p.at) })) });
  const query = (ref) => `network=${encodeURIComponent(ref.network)}&key=${encodeURIComponent(ref.key)}`;
  const when = (t) => new Date(t).toLocaleString([], { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });

  // mini draws a small 24 h graph into el.
  async function mini(el, ref) {
    const d = parse(await api("GET", "/api/pings?range=24h&" + query(ref)));
    if (!d.points.length) { el.innerHTML = `<span class="text-muted text-xs">no ping history yet</span>`; return; }
    const s = d.summary, label = `${s.median == null ? "no reply" : "median " + fmt(s.median)} · ${Math.round(s.answered)}% answered`;
    el.innerHTML = `<svg class="ping-chart" viewBox="0 0 300 54" width="100%" role="img" aria-label="Ping, last 24 hours: ${esc(label)}">${svg(d, { width: 300, height: 54, mini: true })}</svg><div class="text-muted text-xs">last 24 h · ${esc(label)}</div>`;
  }

  // mount fills a section with the controls, summary, graph and list.
  function mount(section) {
    const ref = { network: section.dataset.network, key: section.dataset.key };
    const ranges = [["1h", "1 h"], ["24h", "24 h"], ["7d", "7 d"], ["30d", "30 d"]];
    section.insertAdjacentHTML("beforeend", `
      <div class="mb-3 flex flex-wrap items-center gap-2"><div class="flex gap-1" role="group" aria-label="Range">${ranges.map(([r, t]) => `<button class="btn btn-sm aria-pressed:border-accent aria-pressed:text-accent" type="button" data-range="${r}">${t}</button>`).join("")}</div>
        <label class="check-label"><input type="checkbox" data-log> Log scale</label></div>
      <p class="ping-summary mb-2 flex flex-wrap gap-x-5 gap-y-1"></p>
      <div class="relative"><svg class="ping-chart" height="240" width="100%" tabindex="0" role="img"></svg>
        <div class="ping-tip pointer-events-none absolute top-0 z-1 rounded-md border bg-surface px-2 py-1 text-xs whitespace-nowrap shadow-card" role="status" hidden></div></div>
      <p class="ping-legend mt-1 flex flex-wrap gap-x-4 text-xs text-muted"></p>
      <details class="mt-3"><summary class="cursor-pointer text-accent">All pings</summary>
        <div class="table-wrap mt-2"><table class="data-table"><thead><tr><th>Time</th><th>Result</th><th>Measured from</th></tr></thead><tbody></tbody></table></div>
        <div class="mt-2 flex gap-2"><button class="btn btn-sm newest" type="button" hidden>← Newest</button><button class="btn btn-sm older" type="button" hidden>Older →</button></div></details>`);
    const $ = (s) => section.querySelector(s);
    const chart = $("svg"), tip = $(".ping-tip");
    let range = "24h", log = false, data = null, at = -1, seq = 0;

    function draw() {
      if (!data) return;
      const o = { width: chart.clientWidth || 600, height: 240, log };
      const f = frame(data, o), p = data.points[at];
      let cursor = "";
      if (p) {
        const x = f.x(p.t).toFixed(1);
        cursor = `<line class="cursor" x1="${x}" x2="${x}" y1="${f.m.t}" y2="${f.m.t + f.h}"/>` + (p.ms == null ? "" : `<circle class="mark" cx="${x}" cy="${f.y(p.ms).toFixed(1)}" r="4.5"/>`);
      }
      chart.setAttribute("viewBox", `0 0 ${o.width} ${o.height}`);
      chart.innerHTML = svg(data, o) + cursor + (data.points.length ? "" : `<text x="${o.width / 2}" y="120" text-anchor="middle">No pings in this range</text>`);
      tip.hidden = !p;
      if (!p) return;
      const value = p.ms == null ? "no reply" : data.bucket ? `${fmt(p.ms)} (${fmt(p.min)} – ${fmt(p.max)})` : fmt(p.ms);
      tip.innerHTML = `<b>${esc(value)}</b><br><span class="text-muted">${esc(when(p.t))} · from ${esc(p.origin)}${data.bucket && p.missed ? ` · ${p.missed} of ${p.runs} missed` : ""}</span>`;
      const x = f.x(p.t), w = tip.offsetWidth;
      tip.style.left = Math.max(0, Math.min(o.width - w, x + 12 + w > o.width ? x - w - 12 : x + 12)) + "px";
    }

    async function load() {
      const mine = ++seq;
      const got = parse(await api("GET", `/api/pings?range=${range}&${query(ref)}`));
      if (mine !== seq) return;
      const held = data && data.points[at]?.t;
      data = got;
      at = held == null ? -1 : data.points.findIndex((p) => p.t === held);
      const s = data.summary, item = (k, v) => `<span><span class="text-muted">${k}</span> <b class="tabular-nums">${esc(v)}</b></span>`;
      $(".ping-summary").innerHTML = s.runs ? (s.median == null ? "" : item("Median", fmt(s.median)) + item("Min", fmt(s.min)) + item("Max", fmt(s.max))) + item("Answered", (s.answered < 100 && s.answered > 99 ? s.answered.toFixed(1) : Math.round(s.answered)) + "%") : "";
      $(".ping-legend").innerHTML = (data.bucket ? `<span><i class="mr-1 inline-block h-0.5 w-4 bg-accent align-middle"></i>median</span><span><i class="mr-1 inline-block h-2.5 w-4 bg-accent/20 align-middle"></i>min–max</span>` : "") +
        (data.points.some((p) => p.missed) ? `<span><i class="mr-1 inline-block h-2.5 w-0.5 bg-bad align-middle"></i>no reply</span>` : "");
      chart.setAttribute("aria-label", `Ping times, last ${range}. ${$(".ping-summary").textContent || "No pings"}. Use the arrow keys to read values.`);
      section.querySelectorAll("[data-range]").forEach((b) => b.setAttribute("aria-pressed", String(b.dataset.range === range)));
      draw();
    }
    const tryLoad = () => load().catch((err) => toast("Could not load ping history: " + err.message));

    async function list(before) {
      const d = await api("GET", `/api/pings/list?${query(ref)}${before ? "&before=" + encodeURIComponent(before) : ""}`);
      section.querySelector("tbody").innerHTML = d.rows.map((p) => `<tr><td>${esc(new Date(p.at).toLocaleString())}</td><td class="tabular-nums">${p.ms == null ? `<span class="text-bad">no reply</span>` : esc(fmt(p.ms))}</td><td>${esc(p.origin)}</td></tr>`).join("") || `<tr><td colspan="3" class="text-muted">No pings yet.</td></tr>`;
      $(".older").hidden = !d.next; $(".older").dataset.before = d.next;
      $(".newest").hidden = !before;
    }
    const tryList = (b) => list(b).catch((err) => toast("Could not load pings: " + err.message));

    section.addEventListener("click", (e) => {
      const b = e.target.closest("[data-range]");
      if (b) { range = b.dataset.range; at = -1; tryLoad(); }
      if (e.target.closest(".older")) tryList($(".older").dataset.before);
      if (e.target.closest(".newest")) tryList("");
    });
    $("[data-log]").addEventListener("change", (e) => { log = e.target.checked; draw(); });
    $("details").addEventListener("toggle", (e) => { if (e.target.open) tryList(""); });
    const move = (e) => {
      if (!data) return;
      const box = chart.getBoundingClientRect();
      at = nearest(data.points, frame(data, { width: chart.clientWidth, height: 240, log }).t(e.clientX - box.left));
      draw();
    };
    chart.addEventListener("pointermove", move);
    chart.addEventListener("pointerdown", move);
    chart.addEventListener("pointerleave", () => { at = -1; draw(); });
    chart.addEventListener("blur", () => { at = -1; draw(); });
    chart.addEventListener("keydown", (e) => {
      const d = { ArrowLeft: -1, ArrowRight: 1 }[e.key], n = data?.points.length;
      if (!d || !n) return;
      e.preventDefault();
      at = at < 0 ? n - 1 : Math.max(0, Math.min(n - 1, at + d));
      draw();
    });
    new ResizeObserver(draw).observe(chart);
    tryLoad();
    setInterval(() => load().catch(() => { /* ignore transient errors */ }), 60000);
  }

  const pub = { segments, scale, nearest, fmt, svg, mini, mount };
  if (typeof module !== "undefined" && module.exports) module.exports = pub;
  else { root.PingChart = pub; document.querySelectorAll("[data-ping-history]").forEach(mount); }
})(typeof window !== "undefined" ? window : globalThis);
