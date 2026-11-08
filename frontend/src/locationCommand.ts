import { CommandError, type CommandDispatcher, type PanelId } from "./commands.ts";

// Navigating is a panel action: it reuses DirectoryLoader/ReadDirectory rather
// than creating a second backend navigation or panel-state mechanism.
export function registerLocationCommand(
  dispatcher: CommandDispatcher,
  navigate: (panel: PanelId, location: string, volumeId?: string, home?: boolean) => void,
) {
  dispatcher.register("panel.goToLocation", {
    available: () => ({ enabled: true }), // Home can recover an initially failed directory read.
    execute: request => {
      const params = request.params;
      if (typeof params.location !== "string" || params.location === "" ||
        (params.volumeId !== undefined && typeof params.volumeId !== "string") ||
        (params.home !== undefined && typeof params.home !== "boolean") ||
        (params.home === true && params.volumeId !== undefined)) {
        throw new CommandError("invalid_params", "Не указано расположение для перехода.");
      }
      navigate(request.context.activePanel, params.location, params.volumeId as string | undefined, params.home as boolean | undefined);
    },
  });
}

export function registerPathCommand(
  dispatcher: CommandDispatcher,
  navigate: (panel: PanelId, path: string, baseLocation: string) => void,
) {
  dispatcher.register("panel.goToPath", {
    available: context => context[context.activePanel].directoryLocation
      ? { enabled: true } : { enabled: false, reason: "Каталог ещё не открыт." },
    execute: request => {
      if (typeof request.params.path !== "string" || request.params.path === "") {
        throw new CommandError("invalid_params", "Не указан путь для перехода.");
      }
      const base = request.context[request.context.activePanel].directoryLocation;
      if (!base) throw new CommandError("unavailable", "Каталог ещё не открыт.");
      navigate(request.context.activePanel, request.params.path, base);
    },
  });
}

export function registerHomeCommand(
  dispatcher: CommandDispatcher,
  getHome: () => PromiseLike<{ location: string }>,
  navigate: (panel: PanelId, location: string) => void,
) {
  dispatcher.register("panel.goHome", {
    available: () => ({ enabled: true }),
    execute: async request => {
      const home = await getHome();
      if (!home || typeof home.location !== "string" || home.location === "") {
        throw new CommandError("unavailable", "Домашний каталог недоступен.");
      }
      navigate(request.context.activePanel, home.location);
    },
  });
}
