import test from "node:test";
import assert from "node:assert/strict";
import { fileNameCellStyle } from "../src/fileStyles.ts";

const row = (kind, color) => ({ id: "item", name: "photos.jpg", kind, color, extension: "jpg", size: "",
  modifiedAt: "", navigable: kind === "directory", hidden: false, detail: "" });

test("only ordinary file names receive a configured colour", () => {
  assert.deepEqual(fileNameCellStyle(row("file", "#00AAFF")),
    { color: "var(--marten-selected-name-color, #00AAFF)" });
  assert.deepEqual(fileNameCellStyle(row("directory", "#00AAFF")),
    { color: "var(--marten-selected-name-color, inherit)" });
  assert.deepEqual(fileNameCellStyle({ ...row("directory", "#00AAFF"), parent: true }),
    { color: "var(--marten-selected-name-color, inherit)" });
  assert.deepEqual(fileNameCellStyle(row("symlink", "#00AAFF")),
    { color: "var(--marten-selected-name-color, inherit)" });
  assert.deepEqual(fileNameCellStyle(row("file", "blue")),
    { color: "var(--marten-selected-name-color, inherit)" });
});

test("an unstyled recycled AG Grid cell explicitly clears its previous colour", () => {
  const reused = {};
  Object.assign(reused, fileNameCellStyle(row("file", "#805AD5")));
  assert.equal(reused.color, "var(--marten-selected-name-color, #805AD5)");
  Object.assign(reused, fileNameCellStyle(row("file", "")));
  assert.equal(reused.color, "var(--marten-selected-name-color, inherit)");
});
