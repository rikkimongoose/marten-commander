import { createCommandBackend } from "./commandBackend.ts";
import { CommandError, sourceAndTarget, type CommandDispatcher, type CommandRequest } from "./commands.ts";

export function registerTerminalCommand(dispatcher: CommandDispatcher,
  executeBackend: (request: CommandRequest) => void | PromiseLike<void> = createCommandBackend()) {
  dispatcher.register("terminal.run", {
    available: context => {
      const source = sourceAndTarget(context).source;
      if (source.source === "remote") return { enabled: false, reason: "Командная строка не запускается для SFTP-панели." };
      return source.directoryLocation === null
        ? { enabled: false, reason: "Откройте локальный каталог для запуска команды." } : { enabled: true };
    },
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
