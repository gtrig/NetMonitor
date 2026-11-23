const test = require("node:test");
const assert = require("node:assert");
const O = require("../static/map-options.js");
const mem = (v) => ({ v, getItem(k) { assert.equal(k, "netmon.map.v1"); return this.v ?? null; }, setItem(k, v) { this.v = v; } });

test("defaults", () => {
  assert.deepEqual(O.load(mem()), { layout: "radar", names: true, ips: false, ms: false, ports: false, rings: true, lines: true, offline: true, distance: true });
});
test("round trip", () => {
  const s = mem(), o = { ...O.DEFAULTS, layout: "lanes", ports: true, lines: false };
  O.save(s, o);
  assert.deepEqual(O.load(s), o);
});
test("bad content falls back", () => {
  for (const v of ["{", "null", "[]", "3", '{"layout":"spiral","names":"no"}']) assert.deepEqual(O.load(mem(v)), O.DEFAULTS);
});
test("blocked storage", () => {
  const s = { getItem() { throw new Error("x"); }, setItem() { throw new Error("x"); } };
  assert.deepEqual(O.load(s), O.DEFAULTS);
  O.save(s, O.DEFAULTS);
  assert.deepEqual(O.load(undefined), O.DEFAULTS);
  O.save(undefined, O.DEFAULTS);
});
