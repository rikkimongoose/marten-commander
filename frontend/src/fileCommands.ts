import { createCommandBackend } from "./commandBackend.ts";
import { CANCEL_COMMAND, CommandError, commandErrorMessage, sourceAndTarget, type CommandContext,
  type CommandDispatcher, type CommandRequest, type JsonObject } from "./commands.ts";

type ExecuteFileCommand = (request: CommandRequest) => void | PromiseLike<void>;
export type MkdirPromptResult = { readonly name: string } | typeof CANCEL_COMMAND;
export interface MkdirPrompt {
  open(context: CommandContext): PromiseLike<MkdirPromptResult>;
  retry(message: string): PromiseLike<MkdirPromptResult>;
  complete(): void;
}
export interface MkdirActions {
  readonly prompt: MkdirPrompt;
  readonly succeeded: (request: CommandRequest, name: string) => void;
}

export function directoryNameError(name: string): string {
  if (!name || !name.trim()) return "Введите имя нового каталога.";
  if (name === "." || name === "..") return "Имена «.» и «..» использовать нельзя.";
  if (name.includes("/") || name.includes("\0")) return "Укажите имя одного каталога, без пути и символа «/».";
  return "";
}

function mkdirParams(result: MkdirPromptResult): JsonObject | typeof CANCEL_COMMAND {
  if (result === CANCEL_COMMAND) return result;
  const error = directoryNameError(result.name);
  if (error) throw new CommandError("invalid_params", error);
  return { name: result.name };
}

export function registerFileCommands(
  dispatcher: CommandDispatcher,
  executeBackend: ExecuteFileCommand = createCommandBackend(),
  mkdir?: MkdirActions,
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
  if (mkdir) dispatcher.register("file.mkdir", {
    available: context => sourceAndTarget(context).source.directoryLocation === null
      ? { enabled: false, reason: "Каталог ещё не открыт." } : { enabled: true },
    prepare: async context => mkdirParams(await mkdir.prompt.open(context)),
    execute: async initial => {
      let request = initial;
      for (;;) {
        try {
          await executeBackend(request);
          mkdir.succeeded(request, request.params.name as string);
          mkdir.prompt.complete();
          return;
        } catch (error) {
          const result = await mkdir.prompt.retry(commandErrorMessage(error));
          if (result === CANCEL_COMMAND) {
            mkdir.prompt.complete();
            return;
          }
          request = Object.freeze({ ...initial, params: mkdirParams(result) as JsonObject });
        }
      }
    },
  });
}
