import test from "node:test";
import assert from "node:assert/strict";
import { CommandDispatcher } from "../src/commands.ts";
import { registerFileCommands } from "../src/fileCommands.ts";

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
