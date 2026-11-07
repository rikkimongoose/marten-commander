// Browser fixture only: Vite aliases generated service imports to this module.
// The production App, panels, CommandDispatcher and AG Grid remain unchanged.
const entry = (id: string, directory = false) => ({ id, name: id.split("/").at(-1),
  kind: directory ? "directory" : "file", navigable: directory, hidden: false,
  extension: directory ? "" : "txt", size: directory ? "" : "5", modifiedAt: "2026-01-01T00:00:00.000000000Z", detail: "" });
const home = "/home/test";
const initialRoot = new URLSearchParams(window.location.search).has("emptyRoot");
const state = { reads: [] as string[], hold: "", release: undefined as (() => void) | undefined, emptyRoot: initialRoot };
Object.assign(window, { navigationTest: state });

export async function ReadDirectory(request: { path: string; location: string }) {
  const path = request.location || request.path || (initialRoot ? "/" : home);
  state.reads.push(path);
  if (state.hold === path) await new Promise<void>(resolve => { state.release = resolve; });
  if (path === `${home}/denied`) throw new Error(`Нет прав на чтение каталога ${path}`);
  const parent = path === "/" ? "/" : path.slice(0, path.lastIndexOf("/")) || "/";
  const entries = path === home ? [entry(`${home}/child`, true), entry(`${home}/denied`, true),
    entry(`${home}/one.txt`), entry(`${home}/two.txt`), entry(`${home}/three.txt`)]
    : path === "/home" ? [entry(home, true)]
    : path === "/" && !state.emptyRoot ? [entry("/home", true)]
    : path === `${home}/child` ? [entry(`${path}/nested`, true), entry(`${path}/inside.txt`)] : [];
  return { location: path, parentLocation: parent, displayPath: path, entries,
    initialHome: !initialRoot && !request.path && !request.location, ancestorLocations: path.startsWith(home + "/") ? [home, "/home", "/"] : [parent] };
}

export async function GetHomeDirectory() { return { location: home, displayPath: home }; }
export async function GetVolumeInfo() {
  return { id: "system", name: "System", rootLocation: "/", displayPath: "/", warning: "",
    space: { total: "1000", free: "400", available: "400", used: "600" } };
}
export async function ListVolumes() { return [await GetVolumeInfo()]; }
