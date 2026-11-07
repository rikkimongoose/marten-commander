import { CommandError, sourceAndTarget, type CommandDispatcher, type PanelId } from "./commands.ts";

export function registerNavigationCommands(dispatcher: CommandDispatcher, actions: {
  openEntry(panel: PanelId, directory: string, rowId: string): void;
  parent(panel: PanelId, directory: string): void;
  switchPanel(panel: PanelId): void;
}) {
  const available = (context: Parameters<typeof sourceAndTarget>[0]) =>
    sourceAndTarget(context).source.directoryLocation === null
      ? { enabled: false as const, reason: "Каталог ещё не открыт." } : { enabled: true as const };
  dispatcher.register("panel.openEntry", {
    available,
    execute: request => {
      if (typeof request.params.rowId !== "string" || !request.params.rowId) {
        throw new CommandError("invalid_params", "Не указан элемент для перехода.");
      }
      actions.openEntry(request.context.activePanel, sourceAndTarget(request.context).source.directoryLocation!, request.params.rowId);
    },
  });
  dispatcher.register("panel.goToParent", {
    available,
    execute: request => actions.parent(request.context.activePanel, sourceAndTarget(request.context).source.directoryLocation!),
  });
  dispatcher.register("panel.switch", {
    available: () => ({ enabled: true }),
    execute: request => actions.switchPanel(request.context.activePanel === "left" ? "right" : "left"),
  });
}
