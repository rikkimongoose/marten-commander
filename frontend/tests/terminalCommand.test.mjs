import test from "node:test";
import assert from "node:assert/strict";
import { CommandDispatcher } from "../src/commands.ts";
import { registerTerminalCommand } from "../src/terminalCommand.ts";
import { parseCommandLine } from "../src/commandLine.ts";

test("terminal receives immutable command and active-panel snapshot", async () => {
 const panel = path => ({ directoryLocation: Buffer.from(path).toString("base64url"), selectedLocations: [], focusedLocation: null });
 const context = { activePanel: "left", left: panel("/left"), right: panel("/правый каталог") };
 const calls = []; const gate = Promise.withResolvers();
 const dispatcher = new CommandDispatcher(() => context);
 registerTerminalCommand(dispatcher, async request => { await gate.promise; calls.push(request); });
 const command = `printf '%s' 'a;b' "$HOME" | cat > "файл с пробелами"`;
 const pending = dispatcher.dispatch("terminal.run", { command });
 context.activePanel = "right"; context.left.directoryLocation = "changed";
 gate.resolve(); await pending;
 assert.equal(calls[0].context.activePanel, "left");
 assert.equal(calls[0].context.left.directoryLocation, panel("/left").directoryLocation);
 assert.equal(calls[0].params.command, command);
 await dispatcher.dispatch("terminal.run", { command: "git status" });
 assert.equal(calls[1].context.activePanel, "right");
 await dispatcher.dispatch("terminal.run", { command: "  " }); assert.equal(calls.length, 2);
 await assert.rejects(dispatcher.dispatch("terminal.run", {}));
 context.right.directoryLocation = null;
 await assert.rejects(dispatcher.dispatch("terminal.run", { command: "pwd" }));
 for (const input of ["cd", "cd ..", "cd ~", "cd /tmp", 'cd "some path"']) assert.notEqual(parseCommandLine(input).kind, "external");
});

test("external shell text is preserved verbatim, including heredoc newlines", () => {
 const command = " cat <<'EOF'\nПривет мир\nEOF\n";
 assert.deepEqual(parseCommandLine(command), { kind: "external", command });
});
