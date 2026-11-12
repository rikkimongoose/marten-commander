import { createCommandBackend } from "./commandBackend.ts";
import { CANCEL_COMMAND, CommandError, commandErrorMessage, effectiveSelection, sourceAndTarget, type CommandContext,
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
export type CopyPromptResult = { readonly destination: string } | typeof CANCEL_COMMAND;
export interface CopyPrompt {
  open(context: CommandContext): PromiseLike<CopyPromptResult>;
  retry(message: string): PromiseLike<CopyPromptResult>;
  complete(): void;
}
export interface CopyActions { readonly prompt: CopyPrompt; }
export type MovePromptResult = CopyPromptResult;
export interface MoveActions { readonly prompt: CopyPrompt; }

function copyParams(result: CopyPromptResult): JsonObject | typeof CANCEL_COMMAND {
  if (result === CANCEL_COMMAND) return result;
  if (!result.destination.trim() || !result.destination.startsWith("/")) {
    throw new CommandError("invalid_params", "Укажите абсолютный локальный каталог назначения.");
  }
  return { destination: result.destination };
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
  copy?: CopyActions,
  move?: MoveActions,
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
  dispatcher.register("file.edit", {
    available: context => {
      const source = sourceAndTarget(context).source;
      return source.focusedLocation !== null && source.focusedKind === "file"
        ? { enabled: true } : { enabled: false, reason: "Под курсором нет обычного файла." };
    },
    prepare: context => {
      const source = sourceAndTarget(context).source;
      if (source.focusedLocation === null || source.focusedKind !== "file") {
        throw new CommandError("unavailable", "Под курсором нет обычного файла.");
      }
      return { location: source.focusedLocation };
    },
    execute: request => executeBackend(request),
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
  if (copy) dispatcher.register("file.copy", {
    available: context => {
      const { source, target } = sourceAndTarget(context);
      if (source.directoryLocation === null) return { enabled: false, reason: "Исходный каталог ещё не открыт." };
      if (target.directoryLocation === null) return { enabled: false, reason: "Каталог назначения ещё не открыт." };
      if (effectiveSelection(source).length === 0) return { enabled: false, reason: "Не выбран файл или каталог для копирования." };
      return { enabled: true };
    },
    prepare: async context => copyParams(await copy.prompt.open(context)),
    execute: async initial => {
      let request = initial;
      for (;;) {
        try {
          await executeBackend(request);
          copy.prompt.complete();
          return;
        } catch (error) {
          const result = await copy.prompt.retry(commandErrorMessage(error));
          if (result === CANCEL_COMMAND) {
            copy.prompt.complete();
            return;
          }
          request = Object.freeze({ ...initial, params: copyParams(result) as JsonObject });
        }
      }
    },
  });
  if (move) dispatcher.register("file.move", {
    available: context => {
      const { source, target } = sourceAndTarget(context);
      if (source.directoryLocation === null) return { enabled: false, reason: "Исходный каталог ещё не открыт." };
      if (target.directoryLocation === null) return { enabled: false, reason: "Каталог назначения ещё не открыт." };
      if (effectiveSelection(source).length === 0) return { enabled: false, reason: "Не выбран файл или каталог для перемещения." };
      return { enabled: true };
    },
    prepare: async context => copyParams(await move.prompt.open(context)),
    execute: async initial => {
      let request = initial;
      for (;;) {
        try {
          await executeBackend(request);
          move.prompt.complete();
          return;
        } catch (error) {
          const result = await move.prompt.retry(commandErrorMessage(error));
          if (result === CANCEL_COMMAND) { move.prompt.complete(); return; }
          request = Object.freeze({ ...initial, params: copyParams(result) as JsonObject });
        }
      }
    },
  });
}
