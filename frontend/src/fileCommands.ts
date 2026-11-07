import { createCommandBackend } from "./commandBackend.ts";
import { CommandError, sourceAndTarget, type CommandDispatcher, type CommandRequest } from "./commands.ts";

type ExecuteFileCommand = (request: CommandRequest) => void | PromiseLike<void>;

export function registerFileCommands(
  dispatcher: CommandDispatcher,
  executeBackend: ExecuteFileCommand = createCommandBackend(),
) {
  dispatcher.register("file.open", {
    available: context => sourceAndTarget(context).source.directoryLocation === null
      ? { enabled: false, reason: "Каталог ещё не открыт." } : { enabled: true },
    execute: request => {
      if (typeof request.params.location !== "string" || !request.params.location) {
        throw new CommandError("invalid_params", "Не указан файл для открытия.");
      }
      return executeBackend(request);
    },
  });
}
