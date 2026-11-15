import { createCommandBackend } from "./commandBackend.ts";
import { CommandError, sourceAndTarget, type CommandDispatcher, type CommandRequest } from "./commands.ts";

export function registerTerminalCommand(dispatcher: CommandDispatcher,
  executeBackend: (request: CommandRequest) => void | PromiseLike<void> = createCommandBackend()) {
  dispatcher.register("terminal.run", {
    available: context => sourceAndTarget(context).source.directoryLocation === null
      ? { enabled: false, reason: "Откройте локальный каталог для запуска команды." } : { enabled: true },
    execute: request => {
      const command = request.params.command;
      if (typeof command !== "string" || command.includes("\0")) {
        throw new CommandError("invalid_params", "Требуется текст команды.");
      }
      if (command.trim() === "") return;
      return executeBackend(request);
    },
  });
}
