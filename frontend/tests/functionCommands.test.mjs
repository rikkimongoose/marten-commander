import test from "node:test";
import assert from "node:assert/strict";
import { CommandDispatcher, sourceAndTarget } from "../src/commands.ts";
import { functionCommandForKey, functionCommands, invokeFunctionCommand } from "../src/functionCommands.ts";

const location = value => Buffer.from(value).toString("base64url");
const key = (value, modifiers = {}) => ({ key: value, ctrlKey: false, metaKey: false, altKey: false,
  shiftKey: false, isComposing: false, ...modifiers });
const panel = focusedLocation => ({ directoryLocation: location("/left"), selectedLocations: [], focusedLocation });

test("function bar has the approved F3-F8 commands in order", () => {
  assert.deepEqual(functionCommands, [
    { shortcut: "F3", label: "Просмотр", id: "file.view" },
    { shortcut: "F4", label: "Правка", id: "file.edit" },
    { shortcut: "F5", label: "Копировать", id: "file.copy" },
    { shortcut: "F6", label: "Переместить", id: "file.move" },
    { shortcut: "F7", label: "Каталог", id: "file.mkdir" },
    { shortcut: "F8", label: "Удалить", id: "file.delete" },
  ]);
});

test("F7 button and keyboard paths invoke the same available mkdir command", () => {
  const calls = [];
  const availability = () => ({ enabled: true });
  const dispatch = id => calls.push(id);
  invokeFunctionCommand("file.mkdir", availability, dispatch);
  const shortcut = functionCommandForKey(key("F7"), false);
  assert.equal(shortcut, "file.mkdir");
  invokeFunctionCommand(shortcut, availability, dispatch);
  assert.deepEqual(calls, ["file.mkdir", "file.mkdir"]);
});

test("F5 button and keyboard paths invoke the same copy command", () => {
  const calls = [];
  const availability = () => ({ enabled: true });
  const dispatch = id => calls.push(id);
  invokeFunctionCommand("file.copy", availability, dispatch);
  const shortcut = functionCommandForKey(key("F5"), false);
  assert.equal(shortcut, "file.copy");
  invokeFunctionCommand(shortcut, availability, dispatch);
  assert.deepEqual(calls, ["file.copy", "file.copy"]);
});

test("F6 button and keyboard paths invoke the same move command", () => {
  const calls = [];
  const availability = () => ({ enabled: true });
  const dispatch = id => calls.push(id);
  invokeFunctionCommand("file.move", availability, dispatch);
  const shortcut = functionCommandForKey(key("F6"), false);
  assert.equal(shortcut, "file.move");
  invokeFunctionCommand(shortcut, availability, dispatch);
  assert.deepEqual(calls, ["file.move", "file.move"]);
});

test("missing and context-disabled handlers cannot be invoked and availability reacts to context", async () => {
  let focusedLocation = null;
  let calls = 0;
  const context = () => ({ activePanel: "left", left: panel(focusedLocation), right: panel(null) });
  const dispatcher = new CommandDispatcher(context);
  dispatcher.register("file.view", {
    available: value => sourceAndTarget(value).source.focusedLocation
      ? { enabled: true } : { enabled: false, reason: "Нет текущего файла." },
    execute: () => { calls++; },
  });
  const availability = id => dispatcher.availability(id);
  const dispatch = id => { void dispatcher.dispatch(id); };

  assert.equal(invokeFunctionCommand("file.view", availability, dispatch), false);
  assert.equal(invokeFunctionCommand("file.edit", availability, dispatch), false);
  assert.equal(calls, 0);
  focusedLocation = location("/left/file.txt");
  assert.equal(invokeFunctionCommand("file.view", availability, dispatch), true);
  await Promise.resolve();
  assert.equal(calls, 1);
});

test("function shortcuts leave text input, editing, modal and modified keys alone", () => {
  for (const blocked of [true]) assert.equal(functionCommandForKey(key("F3"), blocked), undefined);
  for (const modifiers of [{ ctrlKey: true }, { metaKey: true }, { altKey: true }, { shiftKey: true }, { isComposing: true }]) {
    assert.equal(functionCommandForKey(key("F4", modifiers), false), undefined);
  }
  assert.equal(functionCommandForKey(key("F2"), false), undefined);
  assert.equal(functionCommandForKey(key("Enter"), false), undefined);
});
