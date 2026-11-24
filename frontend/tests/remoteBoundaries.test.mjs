import test from "node:test";
import assert from "node:assert/strict";
import { CommandDispatcher } from "../src/commands.ts";
import { registerFileCommands } from "../src/fileCommands.ts";
import { registerTerminalCommand } from "../src/terminalCommand.ts";
import { registerTrashCommand } from "../src/trashCommand.ts";

const id = value => Buffer.from(value).toString("base64url");
const local = directory => ({ source: "local", directoryLocation: id(directory), selectedLocations: [id(`${directory}/file`)],
  focusedLocation: id(`${directory}/file`), focusedKind: "file", focusedViewLocation: id(`${directory}/file`) });
const remote = directory => ({ source: "remote", connectionId: "connection-1", directoryLocation: id(directory),
  selectedLocations: [id(`${directory}/file`)], focusedLocation: id(`${directory}/file`), focusedKind: "file",
  focusedViewLocation: id(`${directory}/file`) });
const neverPrompt = { open: () => assert.fail("dialog must not open"), retry: () => assert.fail("retry must not run"), complete() {} };

test("remote active panel rejects local-only handlers, move, mkdir and trash", async () => {
  let backend = 0, preview = 0;
  const context = { activePanel: "left", left: remote("/remote"), right: local("/local") };
  const dispatcher = new CommandDispatcher(() => context);
  registerFileCommands(dispatcher, () => { backend++; }, { prompt: neverPrompt, succeeded() {} },
    undefined, { prompt: neverPrompt }, async () => assert.fail("local viewer must not run"));
  registerTerminalCommand(dispatcher, () => { backend++; });
  registerTrashCommand(dispatcher, { preview: async () => { preview++; return { directory: "", entries: [] }; }, confirm: async () => true },
    () => { backend++; });
  for (const command of ["file.open", "file.edit", "file.mkdir", "file.move", "file.trash", "terminal.run"]) {
    assert.equal(dispatcher.availability(command).enabled, false, command);
    await assert.rejects(dispatcher.dispatch(command, command === "file.open" ? { location: context.left.focusedLocation }
      : command === "terminal.run" ? { command: "pwd" } : {}), error => error.code === "unavailable");
  }
  assert.equal(backend, 0);
  assert.equal(preview, 0);
});

test("remote F3 opens only a captured regular file in the remote viewer", async () => {
  const context = { activePanel: "left", left: remote("/remote/Каталог"), right: local("/local") };
  let opened;
  const dispatcher = new CommandDispatcher(() => context);
  registerFileCommands(dispatcher, () => assert.fail("command backend must not run"), undefined, undefined, undefined,
    async () => assert.fail("local viewer must not run"), undefined, async (connectionId, location) => {
      opened = { connectionId, location };
      context.left.connectionId = "new-session";
      context.left.focusedLocation = id("/remote/later.txt");
      return { id: "viewer", name: "file", path: "/remote/Каталог/file", size: "1", modifiedAt: "", initialMode: "text" };
    });
  assert.equal(dispatcher.availability("file.view").enabled, true);
  await dispatcher.dispatch("file.view");
  assert.deepEqual(opened, { connectionId: "connection-1", location: id("/remote/Каталог/file") });

  for (const kind of ["directory", "symlink", "other"]) {
    context.left.focusedKind = kind;
    assert.equal(dispatcher.availability("file.view").enabled, false);
  }
});

test("remote to local F5 freezes session, selected files and destination before the dialog resolves", async () => {
  const context = { activePanel: "left", left: remote("/remote"), right: local("/local") };
  const answer = Promise.withResolvers();
  let received;
  const dispatcher = new CommandDispatcher(() => context);
  registerFileCommands(dispatcher, request => { received = request; }, undefined, {
    prompt: { open: () => answer.promise, retry: () => assert.fail("no retry"), complete() {} },
  });
  assert.equal(dispatcher.availability("file.copy").enabled, true);
  const pending = dispatcher.dispatch("file.copy");
  context.activePanel = "right";
  context.left.connectionId = "new-session";
  context.left.selectedLocations = [id("/remote/changed")];
  context.right.directoryLocation = id("/other-local");
  answer.resolve({ destination: "/local" });
  assert.equal(await pending, "executed");
  assert.equal(received.context.activePanel, "left");
  assert.equal(received.context.left.connectionId, "connection-1");
  assert.deepEqual(received.context.left.selectedLocations, [id("/remote/file")]);
  assert.equal(received.context.right.directoryLocation, id("/local"));
  assert.deepEqual(received.params, { destination: "/local" });
});

test("F5 and F6 are rejected when a local source targets a remote panel", async () => {
  const context = { activePanel: "left", left: local("/local"), right: remote("/remote") };
  const dispatcher = new CommandDispatcher(() => context);
  registerFileCommands(dispatcher, () => assert.fail("backend must not run"), undefined,
    { prompt: neverPrompt }, { prompt: neverPrompt });
  for (const command of ["file.copy", "file.move"]) {
    assert.equal(dispatcher.availability(command).enabled, false);
    await assert.rejects(dispatcher.dispatch(command), error => error.code === "unavailable");
  }
});

test("F5 and F6 are rejected between remote panels", async () => {
  const context = { activePanel: "left", left: remote("/one"), right: { ...remote("/two"), connectionId: "connection-2" } };
  const dispatcher = new CommandDispatcher(() => context);
  registerFileCommands(dispatcher, () => assert.fail("backend must not run"), undefined,
    { prompt: neverPrompt }, { prompt: neverPrompt });
  for (const command of ["file.copy", "file.move"]) {
    assert.equal(dispatcher.availability(command).enabled, false);
    await assert.rejects(dispatcher.dispatch(command), error => error.code === "unavailable");
  }
});
