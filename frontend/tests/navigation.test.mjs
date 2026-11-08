import test from "node:test";
import assert from "node:assert/strict";
import { CommandDispatcher } from "../src/commands.ts";
import { registerNavigationCommands } from "../src/navigationCommands.ts";
import { panelKeyCommand } from "../src/panelKeyboard.ts";
import { entryDestination, fileLocation, fileSelection, hasParent, panelRows, returnCursor } from "../src/panelRows.ts";
import { compareEntries, restoredIndex, visibleEntries } from "../src/fileSorting.ts";

const entry = (id, navigable = false) => ({ id, name: id, navigable, kind: navigable ? "directory" : "file",
  extension: "", size: "0", modifiedAt: "", hidden: false, detail: "" });
const home = { location: "home", parentLocation: "root", entries: [entry("child", true), entry("file"), { ...entry("hidden"), hidden: true }] };
const root = { location: "root", parentLocation: "root", entries: [entry("home", true)] };
const child = { location: "child", parentLocation: "home", entries: [] };

test(".. is a navigation row, always first in every sort and never selected or sent as a filesystem location", () => {
  const rows = panelRows(home, visibleEntries(home.entries, false));
  assert.deepEqual(rows.map(row => row.id), ["..", "child", "file"]);
  assert.equal(fileLocation(rows[0]), null);
  assert.equal(fileLocation(undefined), null);
  assert.equal(fileLocation(rows[1]), "child");
  assert.equal(fileSelection.mode, "multiRow");
  assert.equal(fileSelection.enableClickSelection, true);
  assert.deepEqual(rows.map(data => fileSelection.isRowSelectable({ data })), [false, true, true]);
  for (const field of ["name", "extension", "size", "modifiedAt"]) {
    for (const descending of [false, true]) {
      const sorted = [...rows].sort((a, b) => compareEntries(a, b, field, descending) * (descending ? -1 : 1));
      assert.equal(sorted[0].id, "..");
    }
  }
  assert.equal(home.entries.length, 3, "synthetic row does not mutate backend entries");
});

test("entry and parent destinations respect root and do not open files (Issue #3)", () => {
  assert.equal(entryDestination(home, "child"), "child");
  assert.equal(entryDestination(child, ".."), "home");
  assert.equal(entryDestination(home, ".."), "root");
  assert.equal(entryDestination(root, ".."), undefined);
  assert.equal(entryDestination(home, "file"), undefined);
  assert.equal(entryDestination(home, "removed"), undefined);
  assert.equal(entryDestination(undefined, ".."), undefined);
  assert.equal(hasParent(root), false);
  assert.deepEqual(panelRows(root, root.entries), root.entries);
  assert.deepEqual(panelRows(undefined, []), []);
});

test("returning up restores the exited directory in the actual sorted list, with missing/hidden fallback", () => {
  const cursor = returnCursor(child, home);
  assert.equal(cursor, "child");
  assert.equal(restoredIndex(["..", "other", "child", "file"], cursor, 0), 2);
  assert.equal(restoredIndex(["..", "other"], cursor, 0), 0);
  assert.equal(returnCursor(root, root), undefined);
  assert.equal(returnCursor(home, child), undefined);
  assert.equal(returnCursor(undefined, home), undefined);
});

const key = (key, modifiers = {}) => ({ key, ctrlKey: false, metaKey: false, shiftKey: false, altKey: false, isComposing: false, ...modifiers });
test("navigation owns only its keys in the file list; text input, IME and AG Grid selection keep theirs", () => {
  for (const [pressed, command] of [["Tab", "panel.switch"], ["Enter", "panel.openEntry"], ["Backspace", "panel.goToParent"]]) {
    assert.equal(panelKeyCommand(key(pressed), false), command);
    assert.equal(panelKeyCommand(key(pressed), true), undefined);
    assert.equal(panelKeyCommand(key(pressed, { isComposing: true }), false), undefined);
  }
  assert.equal(panelKeyCommand(key("Tab", { shiftKey: true }), false), "panel.switch");
  assert.equal(panelKeyCommand(key("a", { ctrlKey: true }), false), "panel.selectAll");
  for (const pressed of ["ArrowUp", "ArrowDown", " "]) {
    for (const modifiers of [{}, { shiftKey: true }, { ctrlKey: true }]) {
      assert.equal(panelKeyCommand(key(pressed, modifiers), false), undefined);
    }
  }
  for (const modifiers of [{ ctrlKey: true }, { altKey: true }, { metaKey: true }]) {
    assert.equal(panelKeyCommand(key("Tab", modifiers), false), undefined);
  }
});

test("commands capture the source panel and directory; Tab targets the opposite panel even before initial reads", async () => {
  const panel = directoryLocation => ({ directoryLocation, focusedLocation: null, selectedLocations: [] });
  const context = { activePanel: "left", left: panel("home"), right: panel("root") };
  const calls = [];
  const dispatcher = new CommandDispatcher(() => context);
  registerNavigationCommands(dispatcher, {
    openEntry: (...args) => calls.push(["open", ...args]), parent: (...args) => calls.push(["up", ...args]),
    switchPanel: (...args) => calls.push(["switch", ...args]),
  });
  const first = dispatcher.dispatch("panel.openEntry", { rowId: "child" });
  context.activePanel = "right";
  await first;
  await dispatcher.dispatch("panel.goToParent");
  await dispatcher.dispatch("panel.switch");
  context.activePanel = "left";
  context.left.directoryLocation = null;
  await dispatcher.dispatch("panel.switch");
  assert.deepEqual(calls, [["open", "left", "home", "child"], ["up", "right", "root"], ["switch", "left"], ["switch", "right"]]);
  await assert.rejects(dispatcher.dispatch("panel.goToParent"), error => error.code === "unavailable");
  context.left.directoryLocation = "home";
  await assert.rejects(dispatcher.dispatch("panel.openEntry", {}), error => error.code === "invalid_params");
});
