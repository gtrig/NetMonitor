const test = require("node:test");
const assert = require("node:assert");
const C = require("../static/ping-chart.js");
const P = (min, ms, origin = "gw") => ({ t: min * 60000, ms, origin, missed: ms == null ? 1 : 0, runs: 1 });

test("segments break at misses, long gaps and a change of origin", () => {
  const s = (ps) => C.segments(ps).map((x) => x.map((p) => p.t / 60000));
  assert.deepEqual(s([P(0, 1), P(5, 2), P(10, null), P(15, 3)]), [[0, 5], [15]]);
  assert.deepEqual(s([P(0, 1), P(5, 1), P(10, 1), P(60, 1), P(65, 1)]), [[0, 5, 10], [60, 65]]);
  assert.deepEqual(s([P(0, 1), P(5, 1, "netmon"), P(10, 1, "netmon")]), [[0], [5, 10]]);
  assert.deepEqual(s([]), []);
  assert.deepEqual(s([P(3, 7)]), [[3]]);
});

test("scale covers the data with round ticks", () => {
  const l = C.scale([1, 87], false);
  assert.ok(l.max >= 87 && l.ticks[0] === 0 && l.ticks.length >= 3 && l.ticks.length <= 7);
  assert.equal(l.frac(0), 0); assert.equal(l.frac(l.max), 1);
  const g = C.scale([0.3, 240], true);
  assert.deepEqual(g.ticks, [0.1, 1, 10, 100, 1000]);
  assert.equal(g.frac(0), 0); assert.ok(Math.abs(g.frac(10) - 0.5) < 1e-9);
  for (const vals of [[], [0], [0, 0]]) for (const log of [false, true]) {
    const s = C.scale(vals, log);
    assert.ok(Number.isFinite(s.frac(0)) && Number.isFinite(s.frac(1)) && s.ticks.length >= 2);
  }
});

test("nearest finds the closest point in time", () => {
  const ps = [P(0, 1), P(5, 2), P(10, null)];
  assert.equal(C.nearest(ps, -50), 0);
  assert.equal(C.nearest(ps, 3 * 60000), 1);
  assert.equal(C.nearest(ps, 99 * 60000), 2);
  assert.equal(C.nearest([], 5), -1);
});

test("format", () => {
  assert.equal(C.fmt(0.42), "0.4 ms"); assert.equal(C.fmt(9.96), "10 ms"); assert.equal(C.fmt(123.4), "123 ms"); assert.equal(C.fmt(0), "0.0 ms"); assert.equal(C.fmt(0.05), "0.05 ms");
});

test("svg escapes nothing into markup and never emits NaN", () => {
  for (const pts of [[], [P(0, 1)], [P(0, null)], [P(0, 1), P(5, null), P(10, 3)]]) for (const log of [false, true]) {
    const s = C.svg({ from: 0, to: 600000, points: pts }, { width: 600, height: 220, log });
    assert.ok(!/NaN|Infinity|undefined/.test(s), s);
  }
  assert.ok(C.svg({ from: 0, to: 600000, points: [P(0, 1), P(5, null)] }, { width: 600, height: 220 }).includes('class="miss"'));
});
