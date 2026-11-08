import test from "node:test";
import assert from "node:assert/strict";
import { parseCommandLine } from "../src/commandLine.ts";
import { CommandDispatcher } from "../src/commands.ts";
import { registerHomeCommand, registerPathCommand } from "../src/locationCommand.ts";

const location = value => Buffer.from(value).toString("base64url");

test("cd parser supports home, parent, absolute, relative and quoted paths", () => {
  assert.deepEqual(parseCommandLine(""), { kind: "empty" });
  assert.deepEqual(parseCommandLine(" cd "), { kind: "home" });
  assert.deepEqual(parseCommandLine("cd .."), { kind: "navigate", path: ".." });
  assert.deepEqual(parseCommandLine("cd /tmp"), { kind: "navigate", path: "/tmp" });
  assert.deepEqual(parseCommandLine("cd Documents"), { kind: "navigate", path: "Documents" });
  assert.deepEqual(parseCommandLine("cd ~/Projects"), { kind: "navigate", path: "~/Projects" });
  assert.deepEqual(parseCommandLine('cd "My Projects"'), { kind: "navigate", path: "My Projects" });
  assert.deepEqual(parseCommandLine("cd 'Other Projects'"), { kind: "navigate", path: "Other Projects" });
  assert.deepEqual(parseCommandLine('cd "quote\\\"name"'), { kind: "navigate", path: 'quote"name' });
});

test("cd is exact and unsupported or shell-like input is never treated as navigation", () => {
  assert.deepEqual(parseCommandLine("cdrom /tmp"), { kind: "external", command: "cdrom /tmp" });
  assert.deepEqual(parseCommandLine("echo hello"), { kind: "external", command: "echo hello" });
  for (const input of ["cd My Projects", 'cd "unfinished', "cd $(pwd)", "cd $HOME", "cd /tmp;rm", "cd `pwd`"]) {
    assert.equal(parseCommandLine(input).kind, "error", input);
  }
});

test("path and Home commands preserve the active-panel snapshot", async () => {
  const base = location("/left");
  const context = { activePanel: "left",
    left: { directoryLocation: base, selectedLocations: [], focusedLocation: null },
    right: { directoryLocation: location("/right"), selectedLocations: [], focusedLocation: null } };
  const calls = [];
  const home = Promise.withResolvers();
  const dispatcher = new CommandDispatcher(() => context);
  registerPathCommand(dispatcher, (...args) => calls.push(["path", ...args]));
  registerHomeCommand(dispatcher, () => home.promise, (...args) => calls.push(["home", ...args]));
  await dispatcher.dispatch("panel.goToPath", { path: "Documents" });
  const pending = dispatcher.dispatch("panel.goHome");
  context.activePanel = "right";
  home.resolve({ location: location("/home/test") });
  await pending;
  assert.deepEqual(calls, [
    ["path", "left", "Documents", base],
    ["home", "left", location("/home/test")],
  ]);
  await assert.rejects(dispatcher.dispatch("panel.goToPath", {}), error => error.code === "invalid_params");
});
