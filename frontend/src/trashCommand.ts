import { createCommandBackend } from "./commandBackend.ts";
import { CANCEL_COMMAND, CommandError, effectiveSelection, sourceAndTarget,
  type CommandContext, type CommandDispatcher, type CommandRequest } from "./commands.ts";

export interface TrashEntry { readonly location: string; readonly name: string; readonly kind: string; readonly revision: string }
export interface TrashPreview { readonly directory: string; readonly entries: readonly TrashEntry[] }
export interface TrashActions {
  preview(context: CommandContext): PromiseLike<TrashPreview>;
  confirm(preview: TrashPreview): PromiseLike<boolean>;
}

export async function prepareTrash(context: CommandContext): Promise<TrashPreview> {
  const { PrepareTrash } = await import("../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/commandservice");
  try {
    const preview = await PrepareTrash({ ...context,
      left: { ...context.left, selectedLocations: [...context.left.selectedLocations] },
      right: { ...context.right, selectedLocations: [...context.right.selectedLocations] },
    });
    return { directory: preview.directory, entries: preview.entries ?? [] };
  } catch (error) {
    const cause = error instanceof Error && "cause" in error ? error.cause : undefined;
    if (cause && typeof cause === "object" && "message" in cause && typeof cause.message === "string") {
      throw new CommandError("trash_failed", cause.message, error);
    }
    throw error;
  }
}

export function registerTrashCommand(dispatcher: CommandDispatcher, actions: TrashActions,
  executeBackend: (request: CommandRequest) => void | PromiseLike<void> = createCommandBackend()) {
  let pending = false;
  dispatcher.register("file.trash", {
    available: context => {
      const source = sourceAndTarget(context).source;
      return source.directoryLocation !== null && effectiveSelection(source).some(location => location !== "..")
        ? { enabled: true } : { enabled: false, reason: "Не выбраны объекты для корзины." };
    },
    prepare: async context => {
      if (pending) return CANCEL_COMMAND;
      pending = true;
      try {
        const locations = effectiveSelection(sourceAndTarget(context).source);
        if (locations.includes("..")) throw new CommandError("invalid_context", "Родительский каталог нельзя переместить в корзину.");
        const preview = await actions.preview(context);
        if (preview.entries.length !== locations.length || preview.entries.some((entry, index) => entry.location !== locations[index])) {
          throw new CommandError("invalid_context", "Список подтверждения не совпадает с выбранными объектами.");
        }
        // Neither the backend response nor the dialog can later alter approval.
        const entries = preview.entries.map(entry => Object.freeze({ ...entry }));
        const frozen = Object.freeze({ directory: preview.directory, entries: Object.freeze(entries) });
        return await actions.confirm(frozen) ? { confirmed: true, entries } : CANCEL_COMMAND;
      } finally { pending = false; }
    },
    execute: request => executeBackend(request),
  });
}
