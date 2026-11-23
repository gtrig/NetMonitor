const test = require("node:test");
const assert = require("node:assert");
const T = require("../static/devices-table.js");

const sorted = (type, dir, vals) => T.order(vals, type, dir).map((i) => vals[i]);

test("addresses sort numerically, IPv4 before IPv6", () => {
  assert.deepEqual(sorted("ip", "asc", ["10.0.0.100", "fe80::1", "10.0.0.9", "9.0.0.1", "2001:db8::2", "2001:db8::10"]),
    ["9.0.0.1", "10.0.0.9", "10.0.0.100", "2001:db8::2", "2001:db8::10", "fe80::1"]);
});

test("IPv6 compression and embedded IPv4 compare by value", () => {
  assert.equal(T.ipKey("::1"), T.ipKey("0:0:0:0:0:0:0:1"));
  assert.equal(T.ipKey("::ffff:1.2.3.4"), T.ipKey("::ffff:102:304"));
  assert.equal(T.ipKey("fe80::1%eth0"), T.ipKey("fe80::1"));
});

test("text sorts naturally and ignores case", () => {
  assert.deepEqual(sorted("text", "asc", ["ether10", "Ether2", "ether1"]), ["ether1", "Ether2", "ether10"]);
});

test("empty values stay last in both directions", () => {
  assert.deepEqual(sorted("text", "asc", ["", "b", "a"]), ["a", "b", ""]);
  assert.deepEqual(sorted("text", "desc", ["", "a", "b"]), ["b", "a", ""]);
  assert.deepEqual(sorted("num", "desc", ["", "0", "10", "9"]), ["10", "9", "0", ""]);
  assert.deepEqual(sorted("ip", "desc", ["", "10.0.0.1"]), ["10.0.0.1", ""]);
});

test("equal values keep their order in both directions", () => {
  assert.deepEqual(T.order(["x", "x", "a"], "text", "desc"), [0, 1, 2]);
  assert.deepEqual(T.order(["x", "x", "a"], "text", "asc"), [2, 0, 1]);
});

test("a header cycles ascending, descending, default order", () => {
  let s = T.nextSort(null, "ip");
  assert.deepEqual(s, { col: "ip", dir: "asc" });
  s = T.nextSort(s, "ip");
  assert.deepEqual(s, { col: "ip", dir: "desc" });
  assert.equal(T.nextSort(s, "ip"), null);
  assert.deepEqual(T.nextSort(s, "name"), { col: "name", dir: "asc" });
});

const mem = (v) => ({ v, getItem() { return this.v; }, setItem(_, x) { this.v = x; } });

test("preferences round-trip", () => {
  const st = mem(null);
  assert.deepEqual(T.load(st), T.defaults());
  T.save(st, { hidden: ["mac"], sort: { col: "ip", dir: "desc" } });
  assert.deepEqual(T.load(st), { hidden: ["mac"], sort: { col: "ip", dir: "desc" } });
});

test("bad or blocked storage gives the defaults", () => {
  for (const bad of ["{", "null", "3", '{"hidden":"mac","sort":{"col":1,"dir":"up"}}']) assert.deepEqual(T.load(mem(bad)), T.defaults());
  assert.deepEqual(T.load({ getItem() { throw new Error("blocked"); } }), T.defaults());
  T.save({ setItem() { throw new Error("blocked"); } }, T.defaults());
});
