import test from "node:test";
import assert from "node:assert/strict";
import { compareEntries, restoredIndex, visibleEntries } from "../src/fileSorting.ts";

const entry = (id, overrides = {}) => ({ id, name: id, kind: "file", extension: "", size: "0",
  modifiedAt: "2026-01-01T00:00:00.000000000Z", navigable: false, hidden: false, detail: "", ...overrides });
// This is AG Grid's contract: it negates the comparator result for descending.
const ordered = (rows, field, descending) => [...rows].sort((a, b) => compareEntries(a, b, field, descending) * (descending ? -1 : 1));

for (const field of ["name", "extension", "size", "modifiedAt"]) {
  for (const descending of [false, true]) {
    test(`${field}, ${descending ? "descending" : "ascending"}: directories always first`, () => {
      const rows = [entry("file"), entry("z-dir", { navigable: true, kind: "directory", size: "" }),
        entry("a-link", { navigable: true, kind: "symlink", size: "" }), entry("another")];
      const result = ordered(rows, field, descending);
      assert.deepEqual(result.map(row => row.navigable), [true, true, false, false]);
      assert.deepEqual(rows.map(row => row.id), ["file", "z-dir", "a-link", "another"]);
    });
  }
}

test("natural name order and deterministic ties", () => {
  const rows = [entry("10", { name: "файл10" }), entry("2", { name: "файл2" }), entry("1", { name: "файл2" })];
  assert.deepEqual(ordered(rows, "name", false).map(row => row.id), ["1", "2", "10"]);
  assert.deepEqual(ordered(rows, "name", true).map(row => row.id), ["10", "2", "1"]);
});

test("extension order with a name tie-breaker", () => {
  const rows = [entry("z.txt", { extension: "txt" }), entry("a.go", { extension: "go" }), entry("README"), entry("b.go", { extension: "go" })];
  assert.deepEqual(ordered(rows, "extension", false).map(row => row.id), ["README", "a.go", "b.go", "z.txt"]);
  assert.deepEqual(ordered(rows, "extension", true).map(row => row.id), ["z.txt", "b.go", "a.go", "README"]);
});

test("sizes remain exact beyond Number.MAX_SAFE_INTEGER", () => {
  const rows = [entry("a-large", { size: "9007199254740993" }), entry("z-small", { size: "9007199254740992" }), entry("empty")];
  assert.deepEqual(ordered(rows, "size", false).map(row => row.id), ["empty", "z-small", "a-large"]);
  assert.deepEqual(ordered(rows, "size", true).map(row => row.id), ["a-large", "z-small", "empty"]);
});

test("timestamps sort including fractional seconds", () => {
  const rows = [entry("a-new", { modifiedAt: "2026-01-01T00:00:00.000000002Z" }), entry("z-old", { modifiedAt: "2026-01-01T00:00:00.000000001Z" })];
  assert.deepEqual(ordered(rows, "modifiedAt", false).map(row => row.id), ["z-old", "a-new"]);
  assert.deepEqual(ordered(rows, "modifiedAt", true).map(row => row.id), ["a-new", "z-old"]);
});

test("hidden filtering preserves provider order and input", () => {
  const rows = [entry("z"), entry(".hidden", { hidden: true }), entry("a")];
  assert.deepEqual(visibleEntries(rows, false).map(row => row.id), ["z", "a"]);
  assert.equal(visibleEntries(rows, true), rows);
  assert.equal(rows.length, 3);
});

test("cursor follows identity, with a nearest-row fallback", () => {
  assert.equal(restoredIndex(["new", "a", "b"], "b", 1), 2);
  assert.equal(restoredIndex(["a", "c"], "deleted", 1), 1);
  assert.equal(restoredIndex(["a"], "deleted", 20), 0);
  assert.equal(restoredIndex([], "b", 1), null);
});
