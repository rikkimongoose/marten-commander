import test from "node:test";
import assert from "node:assert/strict";
import { CANCEL_COMMAND, CommandDispatcher, CommandError } from "../src/commands.ts";
import { directoryNameError, registerFileCommands } from "../src/fileCommands.ts";

const location = value => Buffer.from(value).toString("base64url");

test("file.open uses the explicit row parameter independently of multiple selection", async () => {
  const clicked = location("/left/отчёт с пробелами.txt");
  const context = {
    activePanel: "left",
    left: {
      directoryLocation: location("/left"),
      selectedLocations: [location("/left/one.txt"), location("/left/two.txt")],
      focusedLocation: location("/left/current.txt"),
    },
    right: { directoryLocation: location("/right"), selectedLocations: [], focusedLocation: null },
  };
  let received;
  const dispatcher = new CommandDispatcher(() => context, () => "open-1");
  registerFileCommands(dispatcher, request => { received = request; });
  await dispatcher.dispatch("file.open", { location: clicked });
  assert.equal(received.type, "file.open");
  assert.equal(received.params.location, clicked);
  assert.deepEqual(received.context.left.selectedLocations, context.left.selectedLocations);
  assert.notEqual(received.params.location, received.context.left.focusedLocation);
});

test("file.open validates its concrete row parameter before the backend", async () => {
  const context = {
    activePanel: "left",
    left: { directoryLocation: location("/left"), selectedLocations: [], focusedLocation: null },
    right: { directoryLocation: location("/right"), selectedLocations: [], focusedLocation: null },
  };
  const dispatcher = new CommandDispatcher(() => context);
  registerFileCommands(dispatcher, () => assert.fail("backend must not run"));
  await assert.rejects(dispatcher.dispatch("file.open", {}), error => error.code === "invalid_params");
});

test("mkdir name validation rejects empty, dot and path values but preserves spaces and Unicode", () => {
  for (const name of ["", "   ", ".", "..", "nested/child", "/absolute"]) {
    assert.notEqual(directoryNameError(name), "", name);
  }
  for (const name of ["ordinary", "with spaces", "Кириллица 日本語 🦊", " leading and trailing "]) {
    assert.equal(directoryNameError(name), "", name);
  }
});

test("file.mkdir retains the original panel context across dialog, retry and active-panel switch", async () => {
  const left = location("/left");
  const context = {
    activePanel: "left",
    left: { directoryLocation: left, selectedLocations: [location("/left/selected")], focusedLocation: location("/left/current") },
    right: { directoryLocation: location("/right"), selectedLocations: [], focusedLocation: null },
  };
  const firstName = Promise.withResolvers();
  const calls = [], messages = [], completions = [], refreshed = [];
  const prompt = {
    open: snapshot => { assert.equal(snapshot.activePanel, "left"); return firstName.promise; },
    retry: message => { messages.push(message); return Promise.resolve({ name: "Каталог 🦊" }); },
    complete: () => completions.push(true),
  };
  const dispatcher = new CommandDispatcher(() => context, () => "mkdir-1");
  registerFileCommands(dispatcher, request => {
    calls.push(request);
    if (calls.length === 1) throw new CommandError("mkdir_failed", "Каталог уже существует.");
  }, { prompt, succeeded: (request, name) => refreshed.push([request.context.activePanel, request.context.left.directoryLocation, name]) });

  const pending = dispatcher.dispatch("file.mkdir");
  context.activePanel = "right";
  firstName.resolve({ name: "first" });
  assert.equal(await pending, "executed");
  assert.equal(calls.length, 2);
  assert.ok(calls.every(call => call.context.activePanel === "left" && call.context.left.directoryLocation === left));
  assert.deepEqual(calls.map(call => call.params.name), ["first", "Каталог 🦊"]);
  assert.deepEqual(messages, ["Каталог уже существует."]);
  assert.deepEqual(refreshed, [["left", left, "Каталог 🦊"]]);
  assert.equal(completions.length, 1);
});

test("cancelling mkdir preparation performs no backend operation", async () => {
  const context = {
    activePanel: "left",
    left: { directoryLocation: location("/left"), selectedLocations: [], focusedLocation: null },
    right: { directoryLocation: location("/right"), selectedLocations: [], focusedLocation: null },
  };
  let backendCalls = 0;
  const dispatcher = new CommandDispatcher(() => context);
  registerFileCommands(dispatcher, () => { backendCalls++; }, {
    prompt: { open: () => Promise.resolve(CANCEL_COMMAND), retry: () => assert.fail("no retry"), complete: () => assert.fail("no completion") },
    succeeded: () => assert.fail("no refresh"),
  });
  assert.equal(await dispatcher.dispatch("file.mkdir"), "cancelled");
  assert.equal(backendCalls, 0);
});

test("file.copy freezes explicit selection and destination while panels change", async () => {
  const context = {
    activePanel: "left",
    left: { directoryLocation: location("/left"), selectedLocations: [location("/left/one"), location("/left/two")],
      focusedLocation: location("/left/cursor") },
    right: { directoryLocation: location("/right"), selectedLocations: [], focusedLocation: null },
  };
  const answer = Promise.withResolvers();
  const calls = [], completed = [];
  const dispatcher = new CommandDispatcher(() => context, () => "copy-1");
  registerFileCommands(dispatcher, request => calls.push(request), undefined, {
    prompt: { open: snapshot => { assert.equal(snapshot.activePanel, "left"); return answer.promise; },
      retry: () => assert.fail("no retry"), complete: () => completed.push(true) },
  });
  const pending = dispatcher.dispatch("file.copy");
  context.activePanel = "right";
  context.left.selectedLocations = [location("/left/changed")];
  answer.resolve({ destination: "/edited target/Каталог" });
  assert.equal(await pending, "executed");
  assert.equal(calls.length, 1);
  assert.equal(calls[0].context.activePanel, "left");
  assert.deepEqual(calls[0].context.left.selectedLocations, [location("/left/one"), location("/left/two")]);
  assert.deepEqual(calls[0].params, { destination: "/edited target/Каталог" });
  assert.equal(completed.length, 1);
});

test("file.copy falls back to cursor and cancellation has no side effects", async () => {
  const context = { activePanel: "left",
    left: { directoryLocation: location("/left"), selectedLocations: [], focusedLocation: location("/left/cursor") },
    right: { directoryLocation: location("/right"), selectedLocations: [], focusedLocation: null } };
  let backendCalls = 0;
  const dispatcher = new CommandDispatcher(() => context);
  registerFileCommands(dispatcher, () => { backendCalls++; }, undefined, {
    prompt: { open: snapshot => { assert.equal(snapshot.left.focusedLocation, location("/left/cursor")); return CANCEL_COMMAND; },
      retry: () => assert.fail("no retry"), complete: () => assert.fail("no completion") },
  });
  assert.equal(dispatcher.availability("file.copy").enabled, true);
  assert.equal(await dispatcher.dispatch("file.copy"), "cancelled");
  assert.equal(backendCalls, 0);
  context.left.focusedLocation = null;
  assert.equal(dispatcher.availability("file.copy").enabled, false);
});
