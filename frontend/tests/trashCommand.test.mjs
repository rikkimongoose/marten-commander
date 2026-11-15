import test from "node:test";
import assert from "node:assert/strict";
import { CommandDispatcher } from "../src/commands.ts";
import { registerTrashCommand } from "../src/trashCommand.ts";
import { functionCommandForKey, invokeFunctionCommand } from "../src/functionCommands.ts";
import { operationRefreshLocations } from "../src/operationRefresh.ts";

const panel = (directory, selected = [], focused = null) => ({ directoryLocation: directory, selectedLocations: selected, focusedLocation: focused });
const entry = location => ({ location, name: location, kind: "file", revision: "original" });
const key = { key: "F8", ctrlKey: false, altKey: false, metaKey: false, shiftKey: false, isComposing: false };

test("F8 and button share file.trash; text fields and dialogs block shortcuts", () => {
 const calls = [];
 const shortcut = functionCommandForKey(key, false);
 assert.equal(shortcut, "file.trash");
 invokeFunctionCommand(shortcut, () => ({ enabled: true }), id => calls.push(id));
 invokeFunctionCommand("file.trash", () => ({ enabled: true }), id => calls.push(id));
 assert.deepEqual(calls, ["file.trash", "file.trash"]);
 assert.equal(functionCommandForKey(key, true), undefined);
});

test("trash freezes selection before confirmation and requires explicit approval", async () => {
 const context = { activePanel: "left", left: panel("/left", ["one", "two"], "cursor"), right: panel("/right") };
 const dispatcher = new CommandDispatcher(() => context);
 const confirmation = Promise.withResolvers(); const opened = Promise.withResolvers(); const calls = [];
 registerTrashCommand(dispatcher, {
  preview: async snapshot => ({ directory: "/left", entries: snapshot.left.selectedLocations.map(entry) }),
  confirm: preview => { opened.resolve(preview); return confirmation.promise; },
 }, request => calls.push(request));
 const pending = dispatcher.dispatch("file.trash");
 const preview = await opened.promise;
 assert.equal(calls.length, 0);
 assert.deepEqual(preview.entries.map(e => e.name), ["one", "two"]);
 assert.ok(Object.isFrozen(preview.entries[0]));
 context.activePanel = "right"; context.left.selectedLocations.splice(0, 2, "replacement");
 context.left.directoryLocation = "/changed";
 assert.equal(await dispatcher.dispatch("file.trash").catch(e => e.code), "unavailable");
 confirmation.resolve(true); await pending;
 assert.equal(calls.length, 1);
 assert.equal(calls[0].type, "file.trash");
 assert.equal(calls[0].context.activePanel, "left");
 assert.equal(calls[0].context.left.directoryLocation, "/left");
 assert.deepEqual(calls[0].params.entries.map(e => e.location), ["one", "two"]);
});

test("cancel, empty selection and parent never call mutation; cursor fallback works", async () => {
 const context = { activePanel: "left", left: panel("/left", [], "cursor"), right: panel("/right") };
 const dispatcher = new CommandDispatcher(() => context); let approve = false; const calls = [];
 registerTrashCommand(dispatcher, { preview: async snapshot => ({ directory: "/left", entries: [entry(snapshot.left.focusedLocation)] }),
  confirm: async () => approve }, request => calls.push(request));
 assert.equal(await dispatcher.dispatch("file.trash"), "cancelled"); assert.equal(calls.length, 0);
 approve = true; await dispatcher.dispatch("file.trash"); assert.equal(calls[0].params.entries[0].location, "cursor");
 context.left.focusedLocation = null; assert.equal(dispatcher.availability("file.trash").enabled, false);
 context.left.focusedLocation = ".."; assert.equal(dispatcher.availability("file.trash").enabled, false);
});

test("trash refreshes captured source for completed, failed and cancelled results, never navigates target", () => {
 for (const state of ["completed", "failed", "cancelled"]) {
  assert.deepEqual(operationRefreshLocations({ type: "file.trash", state,
   context: { sourceDirectory: "original", destinationDirectory: "other" } }), ["original"]);
 }
});
