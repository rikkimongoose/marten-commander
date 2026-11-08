// Browser fixture only: Vite aliases generated service imports to this module.
// The production App, panels, CommandDispatcher and AG Grid remain unchanged.
const entry = (id: string, directory = false) => ({ id, name: id.split("/").at(-1),
  kind: directory ? "directory" : "file", navigable: directory, hidden: false,
  extension: directory ? "" : "txt", size: directory ? "" : "5", modifiedAt: "2026-01-01T00:00:00.000000000Z", detail: "" });
const home = "/home/test";
const initialRoot = new URLSearchParams(window.location.search).has("emptyRoot");
const state = { reads: [] as string[], opens: [] as any[], openError: "", hold: "",
  release: undefined as (() => void) | undefined, emptyRoot: initialRoot,
  mkdirs: [] as { parent: string; name: string }[], created: new Map<string, string[]>(), holdMkdir: false,
  releaseMkdir: undefined as (() => void) | undefined };
Object.assign(window, { navigationTest: state });

function resolve(path: string, base: string) {
  if (path === "~" || path === "~/") return home;
  if (path.startsWith("~/")) path = `${home}/${path.slice(2)}`;
  const source = path.startsWith("/") ? path : `${base}/${path}`;
  const parts: string[] = [];
  for (const part of source.split("/")) {
    if (part === "" || part === ".") continue;
    if (part === "..") parts.pop(); else parts.push(part);
  }
  return `/${parts.join("/")}`;
}

export async function ReadDirectory(request: { path: string; location: string; baseLocation?: string }) {
  const path = request.location || (request.path ? resolve(request.path, request.baseLocation || "/") : "") || (initialRoot ? "/" : home);
  state.reads.push(path);
  if (state.hold === path) await new Promise<void>(resolve => { state.release = resolve; });
  if (path === `${home}/denied`) throw new Error(`Нет прав на чтение каталога ${path}`);
  const parent = path === "/" ? "/" : path.slice(0, path.lastIndexOf("/")) || "/";
  const entries = path === home ? [entry(`${home}/child`, true), entry(`${home}/denied`, true), entry(`${home}/My Projects`, true),
    entry(`${home}/one.txt`), entry(`${home}/two.txt`), entry(`${home}/three.txt`)]
    : path === "/home" ? [entry(home, true)]
    : path === "/" && !state.emptyRoot ? [entry("/home", true)]
    : path === `${home}/child` ? [entry(`${path}/nested`, true), entry(`${path}/inside.txt`)] : [];
  entries.push(...(state.created.get(path) ?? []).map(name => entry(`${path}/${name}`, true)));
  return { location: path, parentLocation: parent, displayPath: path, entries,
    initialHome: !initialRoot && !request.path && !request.location, ancestorLocations: path.startsWith(home + "/") ? [home, "/home", "/"] : [parent] };
}

export async function GetHomeDirectory() { return { location: home, displayPath: home }; }
export async function GetVolumeInfo() {
  return { id: "system", name: "System", rootLocation: "/", displayPath: "/", warning: "",
    space: { total: "1000", free: "400", available: "400", used: "600" } };
}
export async function ListVolumes() { return [await GetVolumeInfo()]; }
export async function ListOperations() { return []; }
export async function CancelOperation() { throw new Error("Test operation not found"); }
export async function ExecuteCommand(request: any) {
  state.opens.push(request);
  if (request.type === "file.mkdir") {
    if (state.holdMkdir) await new Promise<void>(resolve => { state.releaseMkdir = resolve; });
    const panel = request.context[request.context.activePanel];
    const parent = panel.directoryLocation;
    const name = request.params.name;
    const names = state.created.get(parent) ?? [];
    if (names.includes(name)) throw new Error("Wails command failed", {
      cause: { code: "mkdir_failed", message: `Каталог «${name}» уже существует.` },
    });
    names.push(name);
    state.created.set(parent, names);
    state.mkdirs.push({ parent, name });
    return;
  }
  if (state.openError) throw new Error("Wails command failed", {
    cause: { code: "open_failed", message: state.openError },
  });
}
