import type { Directory, DirectoryRequest, HomeDirectory, Volume } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/models";
import { CommandError } from "./commands.ts";

export type PanelNavigationRequest = DirectoryRequest & { volumeId?: string; home?: boolean };
type Call<T> = PromiseLike<T> & { cancel?: () => void | PromiseLike<void> };

// Called only for the latest successful directory read. Failed/obsolete choices
// must not replace the shortcut associated with the visible snapshot.
export function selectedHomeAfterNavigation(
  previous: HomeDirectory | undefined, directory: Directory, request: PanelNavigationRequest,
): HomeDirectory | undefined {
  const initialHome = request.path === "" && request.location === "" && directory.initialHome;
  if (request.home || initialHome) return { location: directory.location, displayPath: directory.displayPath };
  if (request.volumeId !== undefined) return undefined;
  return previous && (directory.location === previous.location || directory.ancestorLocations?.includes(previous.location))
    ? previous : undefined;
}

// Validation and reading form one DirectoryLoader request. Its existing version
// guard handles newer volume choices, manual paths, cancellation and unmounts.
export function createPanelReader(
  read: (request: DirectoryRequest) => Call<Directory>,
  lookup: (request: DirectoryRequest) => Call<Volume>,
): (request: PanelNavigationRequest) => Call<Directory> {
  return request => {
    const address = { path: request.path, location: request.location, baseLocation: request.baseLocation ?? "" };
    if (request.volumeId === undefined) return read(address);
    let cancelled = false;
    const check = lookup(address);
    let active: { cancel?: () => void | PromiseLike<void> } = check;
    const promise = (async () => {
      const volume = await check;
      if (cancelled) throw new CommandError("unavailable", "Переход отменён.");
      if (volume.id !== request.volumeId || volume.rootLocation !== request.location) {
        throw new CommandError("unavailable", "Том отключён или заменён. Откройте список устройств заново.");
      }
      const result = read(address);
      active = result;
      return await result;
    })();
    return Object.assign(promise, { cancel: () => { cancelled = true; return active.cancel?.(); } });
  };
}
