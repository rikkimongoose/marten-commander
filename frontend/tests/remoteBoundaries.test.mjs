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

test("remote active panel rejects local file handlers, terminal, viewer, mkdir and trash", async () => {
  let backend = 0, viewer = 0, preview = 0;
  const context = { activePanel: "left", left: remote("/remote"), right: local("/local") };
  const dispatcher = new CommandDispatcher(() => context);
  registerFileCommands(dispatcher, () => { backend++; }, { prompt: neverPrompt, succeeded() {} },
    { prompt: neverPrompt }, { prompt: neverPrompt }, async () => { viewer++; });
  registerTerminalCommand(dispatcher, () => { backend++; });
  registerTrashCommand(dispatcher, { preview: async () => { preview++; return { directory: "", entries: [] }; }, confirm: async () => true },
    () => { backend++; });
  for (const command of ["file.open", "file.view", "file.edit", "file.mkdir", "file.copy", "file.move", "file.trash", "terminal.run"]) {
    assert.equal(dispatcher.availability(command).enabled, false, command);
    await assert.rejects(dispatcher.dispatch(command, command === "file.open" ? { location: context.left.focusedLocation }
      : command === "terminal.run" ? { command: "pwd" } : {}), error => error.code === "unavailable");
  }
  assert.equal(backend, 0);
  assert.equal(viewer, 0);
  assert.equal(preview, 0);
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
