import { CommandError, type CommandDispatcher, type PanelId } from "./commands.ts";

// Navigating is a panel action: it reuses DirectoryLoader/ReadDirectory rather
// than creating a second backend navigation or panel-state mechanism.
export function registerLocationCommand(
  dispatcher: CommandDispatcher,
  navigate: (panel: PanelId, location: string, volumeId?: string) => void,
) {
  dispatcher.register("panel.goToLocation", {
    available: () => ({ enabled: true }), // Home can recover an initially failed directory read.
    execute: request => {
      const params = request.params;
      if (typeof params.location !== "string" || params.location === "" ||
        (params.volumeId !== undefined && typeof params.volumeId !== "string")) {
        throw new CommandError("invalid_params", "Не указано расположение для перехода.");
      }
      navigate(request.context.activePanel, params.location, params.volumeId as string | undefined);
    },
  });
}
