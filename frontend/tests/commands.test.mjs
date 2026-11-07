import test from "node:test";
import assert from "node:assert/strict";
import { CANCEL_COMMAND, CommandDispatcher, CommandError, commandErrorMessage, effectiveSelection, snapshotContext, sourceAndTarget } from "../src/commands.ts";
import { createCommandBackend } from "../src/commandBackend.ts";

const location = value => Buffer.from(value).toString("base64url");
function context(activePanel = "left") {
  return {
    activePanel,
    left: { directoryLocation: location("/left"), selectedLocations: [location("/left/selected")], focusedLocation: location("/left/focused") },
    right: { directoryLocation: location("/right"), selectedLocations: [], focusedLocation: null },
  };
}
const available = () => ({ enabled: true });
const hasCode = code => error => error instanceof CommandError && error.code === code;

for (const active of ["left", "right"]) {
  test(`source/target follow active ${active}, independently of selection and focus`, () => {
    const original = context(active);
    const snapshot = snapshotContext(original);
    const { source, target } = sourceAndTarget(snapshot);
    assert.deepEqual(source, original[active]);
    assert.deepEqual(target, original[active === "left" ? "right" : "left"]);
    assert.deepEqual(snapshot.right.selectedLocations, []);
    assert.notEqual(snapshot.left.selectedLocations[0], snapshot.left.focusedLocation);
  });
}

test("snapshot copies and freezes both panels and selections, without retaining UI objects", () => {
  const original = context();
  original.left.grid = { mutable: true };
  const snapshot = snapshotContext(original);
  const saved = JSON.stringify(snapshot);
  original.activePanel = "right";
  original.left.directoryLocation = location("/other");
  original.left.selectedLocations.push(location("/other/new"));
  original.left.focusedLocation = null;
  original.right.selectedLocations.push(location("/right/new"));
  assert.equal(JSON.stringify(snapshot), saved);
  assert.equal("grid" in snapshot.left, false);
  assert.throws(() => { snapshot.activePanel = "right"; }, TypeError);
  assert.throws(() => { snapshot.left.directoryLocation = "changed"; }, TypeError);
  assert.throws(() => snapshot.left.selectedLocations.push("changed"), TypeError);
  assert.throws(() => snapshot.right.selectedLocations.push("changed"), TypeError);
});

test("context keeps explicit selection and cursor separate", () => {
  const input = context();
  input.left.selectedLocations = [];
  assert.deepEqual(snapshotContext(input).left, input.left);
});

test("commands use explicit selection before the independent cursor", () => {
  const panel = snapshotContext(context()).left;
  assert.deepEqual(effectiveSelection(panel), [location("/left/selected")]);
  assert.notEqual(effectiveSelection(panel)[0], panel.focusedLocation);
});

test("commands fall back to the cursor only when explicit selection is empty", () => {
  const input = context();
  input.left.selectedLocations = [];
  const panel = snapshotContext(input).left;
  assert.deepEqual(effectiveSelection(panel), [location("/left/focused")]);
  assert.throws(() => effectiveSelection(panel).push("changed"), TypeError);
  assert.deepEqual(effectiveSelection({ ...panel, focusedLocation: null }), []);
});

test("snapshot is captured once before a dialog and reused after UI changes", async () => {
  const original = context();
  let reads = 0, prepared, executed;
  const dialog = Promise.withResolvers();
  const dispatcher = new CommandDispatcher(() => { reads++; return original; }, () => "call-1");
  dispatcher.register("test.dialog", {
    available,
    prepare: snapshot => { prepared = snapshot; return dialog.promise; },
    execute: request => { executed = request; },
  });
  const pending = dispatcher.dispatch("test.dialog");
  const saved = JSON.stringify(prepared);
  original.activePanel = "right";
  original.left.directoryLocation = location("/elsewhere");
  original.left.selectedLocations.length = 0;
  original.right.selectedLocations.push(location("/right/new"));
  const params = { name: "confirmed", options: { flags: ["one"] } };
  dialog.resolve(params);
  assert.equal(await pending, "executed");
  params.options.flags.push("two");
  assert.equal(reads, 1);
  assert.equal(executed.context, prepared);
  assert.equal(JSON.stringify(executed.context), saved);
  assert.deepEqual(executed.params, { name: "confirmed", options: { flags: ["one"] } });
  assert.throws(() => executed.params.options.flags.push("three"), TypeError);
  assert.equal(executed.id, "call-1");
  assert.equal(executed.type, "test.dialog");
});

test("an asynchronous handler retains its original context while another command runs", async () => {
  const original = context();
  const gate = Promise.withResolvers();
  const received = [];
  const dispatcher = new CommandDispatcher(() => original);
  dispatcher.register("test.async", {
    available,
    execute: async request => { await gate.promise; received.push(request); },
  });
  const first = dispatcher.dispatch("test.async");
  original.activePanel = "right";
  original.left.selectedLocations.length = 0;
  const second = dispatcher.dispatch("test.async");
  gate.resolve();
  await Promise.all([first, second]);
  assert.equal(received[0].context.activePanel, "left");
  assert.equal(received[0].context.left.selectedLocations.length, 1);
  assert.equal(received[1].context.activePanel, "right");
  assert.notEqual(received[0].id, received[1].id);
});

test("button, shortcut and menu dispatch the same registered frontend handler without Go", async () => {
  let calls = 0, backendCalls = 0;
  const backend = createCommandBackend(() => { backendCalls++; return Promise.resolve(); });
  const dispatcher = new CommandDispatcher(context);
  dispatcher.register("panel.selectAll", { available, execute: () => { calls++; } });
  dispatcher.register("test.backend", { available, execute: backend });
  const button = () => dispatcher.dispatch("panel.selectAll");
  const shortcut = () => dispatcher.dispatch("panel.selectAll");
  const menu = () => dispatcher.dispatch("panel.selectAll");
  await button(); await shortcut(); await menu();
  assert.equal(calls, 3);
  assert.equal(backendCalls, 0);
});

test("cancelled preparation calls neither handler nor Wails binding", async () => {
  let calls = 0;
  const dispatcher = new CommandDispatcher(context);
  dispatcher.register("test.dialog", {
    available, prepare: async () => CANCEL_COMMAND,
    execute: createCommandBackend(() => { calls++; return Promise.resolve(); }),
  });
  assert.equal(await dispatcher.dispatch("test.dialog"), "cancelled");
  assert.equal(calls, 0);
});

test("backend adapter sends exactly id/type/context/params to the generated binding seam", async () => {
  let received;
  const original = context("right");
  const dispatcher = new CommandDispatcher(() => original, () => "invocation-123");
  dispatcher.register("test.backend", {
    available,
    prepare: () => ({ name: "test", count: 2 }),
    execute: createCommandBackend(request => { received = request; return Promise.resolve(); }),
  });
  await dispatcher.dispatch("test.backend");
  assert.deepEqual(received, { id: "invocation-123", type: "test.backend", context: original, params: { name: "test", count: 2 } });
  received.context.left.selectedLocations.push("binding-owned");
  assert.equal(original.left.selectedLocations.length, 1);
});

test("unknown and disabled commands reject before preparation or execution", async () => {
  let calls = 0, reads = 0;
  const dispatcher = new CommandDispatcher(() => { reads++; return context(); });
  dispatcher.register("test.disabled", {
    available: () => ({ enabled: false, reason: "Недоступно" }),
    prepare: () => { calls++; return {}; },
    execute: () => { calls++; },
  });
  await assert.rejects(dispatcher.dispatch("missing"), hasCode("unknown_command"));
  assert.equal(reads, 0);
  assert.deepEqual(dispatcher.availability("test.disabled"), { enabled: false, reason: "Недоступно" });
  assert.equal(dispatcher.availability("missing").enabled, false);
  await assert.rejects(dispatcher.dispatch("test.disabled"), hasCode("unavailable"));
  assert.equal(calls, 0);
});

test("availability is rechecked on dispatch and duplicate IDs cannot replace handlers", async () => {
  let enabled = true, calls = 0;
  const dispatcher = new CommandDispatcher(context);
  const definition = { available: () => enabled ? { enabled: true } : { enabled: false, reason: "Changed" }, execute: () => { calls++; } };
  dispatcher.register("test.command", definition);
  assert.equal(dispatcher.availability("test.command").enabled, true);
  enabled = false;
  await assert.rejects(dispatcher.dispatch("test.command"), hasCode("unavailable"));
  assert.throws(() => dispatcher.register("test.command", { available, execute() {} }), hasCode("duplicate_command"));
  enabled = true;
  await dispatcher.dispatch("test.command");
  assert.equal(calls, 1);
});

for (const phase of ["available", "prepare", "execute"]) {
  test(`errors in ${phase} propagate as typed errors with a displayable message`, async () => {
    const original = new Error("Test failure");
    const dispatcher = new CommandDispatcher(context);
    const definition = { available, prepare: () => ({}), execute() {} };
    definition[phase] = () => { throw original; };
    dispatcher.register("test.error", definition);
    await assert.rejects(dispatcher.dispatch("test.error"), error => {
      assert.equal(error.code, "handler_failed");
      assert.equal(error.cause, original);
      assert.match(commandErrorMessage(error), /Test failure/);
      return true;
    });
  });
}

test("async binding rejection preserves typed Go errors and their message", async () => {
  const failure = new Error("Wails call failed", { cause: { code: "unknown_command", message: "Команда не поддерживается" } });
  const dispatcher = new CommandDispatcher(context);
  dispatcher.register("test.backend", { available, execute: createCommandBackend(async () => { throw failure; }) });
  await assert.rejects(dispatcher.dispatch("test.backend"), error => {
    assert.equal(error.code, "unknown_command");
    assert.equal(error.cause, failure);
    assert.equal(commandErrorMessage(error), "Команда не поддерживается");
    return true;
  });
});

test("transport errors and rejected dialogs propagate without a successful result", async () => {
  for (const phase of ["prepare", "execute"]) {
    const dispatcher = new CommandDispatcher(context);
    const failure = new Error("Connection failed");
    const reject = async () => { throw failure; };
    dispatcher.register("test.error", { available, prepare: phase === "prepare" ? reject : undefined,
      execute: createCommandBackend(reject) });
    await assert.rejects(dispatcher.dispatch("test.error"), error => error.code === "handler_failed" && error.cause === failure);
  }
});

test("malformed contexts and non-JSON dialog parameters never reach the handler", async () => {
  for (const invalid of [{ ...context(), activePanel: "other" }, { ...context(), left: null }]) {
    const dispatcher = new CommandDispatcher(() => invalid);
    dispatcher.register("test.command", { available, execute: () => assert.fail("must not execute") });
    await assert.rejects(dispatcher.dispatch("test.command"), hasCode("invalid_context"));
  }
  const cycle = {}; cycle.self = cycle;
  for (const params of [null, [], { value: undefined }, { value: Infinity }, { value: new Date() }, cycle]) {
    const dispatcher = new CommandDispatcher(context);
    dispatcher.register("test.command", { available, prepare: () => params, execute: () => assert.fail("must not execute") });
    await assert.rejects(dispatcher.dispatch("test.command"), hasCode("invalid_params"));
  }
});
