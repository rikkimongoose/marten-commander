// Browser fixture only: Vite aliases generated service imports to this module.
// The production App, panels, CommandDispatcher and AG Grid remain unchanged.
const entry = (id: string, directory = false) => ({ id, name: id.split("/").at(-1),
  kind: directory ? "directory" : "file", navigable: directory, hidden: false,
  extension: directory ? "" : "txt", size: directory ? "" : "5", modifiedAt: "2026-01-01T00:00:00.000000000Z", detail: "" });
const home = "/home/test";
const query = new URLSearchParams(window.location.search);
const initialRoot = query.has("emptyRoot");
const state = { trashed: new Set<string>(), previews: [] as any[], reads: [] as string[], opens: [] as any[], openError: "", hold: "",
  release: undefined as (() => void) | undefined, emptyRoot: initialRoot,
  mkdirs: [] as { parent: string; name: string }[], created: new Map<string, string[]>(), holdMkdir: false,
  releaseMkdir: undefined as (() => void) | undefined, operations: [] as any[], conflictResolutions: [] as any[],
  viewerReads: [] as string[], viewerBinaryReads: [] as { offset: string; length: number }[], viewerMode: query.get("viewerMode") ?? "text",
  viewerBroken: query.has("viewerBroken"), viewerClosed: 0, viewerWindows: [] as string[],
  sftpProfiles: [] as any[], sftpAttempts: new Map<string, any>(), sftpSecrets: [] as string[], sftpCancels: 0,
  directorySizeRequests: [] as string[], directorySizeResolvers: new Map<string, (value: any) => void>() };
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
  return { location: path, parentLocation: parent, displayPath: path, entries: entries.filter(item => !state.trashed.has(item.id)),
    initialHome: !initialRoot && !request.path && !request.location, ancestorLocations: path.startsWith(home + "/") ? [home, "/home", "/"] : [parent] };
}
export function CalculateDirectorySize(location: string) {
  state.directorySizeRequests.push(location);
  const result = new Promise<any>(resolve => state.directorySizeResolvers.set(location, resolve)) as Promise<any> & { cancel: () => Promise<void> };
  result.cancel = async () => {};
  return result;
}

export async function GetHomeDirectory() { return { location: home, displayPath: home }; }
export async function GetVolumeInfo() {
  return { id: "system", name: "System", rootLocation: "/", displayPath: "/", warning: "",
    space: { total: "1000", free: "400", available: "400", used: "600" } };
}
export async function ListVolumes() { return [await GetVolumeInfo()]; }
export async function ListProfiles() { return state.sftpProfiles.map(profile => ({ ...profile })); }
export async function SaveProfile(profile: any) {
  const saved = { ...profile, id: profile.id || `profile-${state.sftpProfiles.length + 1}` };
  const index = state.sftpProfiles.findIndex(item => item.id === saved.id);
  if (index < 0) state.sftpProfiles.push(saved); else state.sftpProfiles[index] = saved;
  return { ...saved };
}
export async function DeleteProfile(id: string) { state.sftpProfiles = state.sftpProfiles.filter(profile => profile.id !== id); }
export async function Connect(request: any) {
  state.sftpSecrets.push(request.secret);
  const status = { attemptId: `attempt-${state.sftpAttempts.size + 1}`, profileId: request.profileId, state: "connecting" };
  state.sftpAttempts.set(status.attemptId, status);
  return { ...status };
}
export async function GetConnectionStatus(id: string) {
  const status = state.sftpAttempts.get(id);
  if (status.state === "connecting") Object.assign(status, { state: "awaiting_host_key", challenge: { id: "challenge-1",
    host: "sftp.example", port: 2222, algorithm: "ssh-ed25519", fingerprint: "SHA256:test-fingerprint", expiresAt: "2099-01-01T00:00:00Z" } });
  return structuredClone(status);
}
export async function ResolveHostKey(attemptID: string, challengeID: string, accept: boolean) {
  const status = state.sftpAttempts.get(attemptID);
  if (challengeID !== status.challenge.id) throw new Error("stale challenge");
  Object.assign(status, accept ? { state: "connected", connectionId: "connection-1", challenge: undefined }
    : { state: "error", error: "Ключ отклонён", challenge: undefined });
}
export async function CancelConnection(id: string) { state.sftpCancels++; Object.assign(state.sftpAttempts.get(id), { state: "cancelled" }); }
export async function Disconnect(_id: string) {}
export function ReadRemoteDirectory(request: { connectionId: string; path: string; location: string }) {
  const decode = (value: string) => new TextDecoder().decode(Uint8Array.from(atob(value.replace(/-/g, "+").replace(/_/g, "/")), char => char.charCodeAt(0)));
  const encode = (value: string) => btoa(String.fromCharCode(...new TextEncoder().encode(value))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  const remotePath = request.path || (request.location ? decode(request.location) : "/");
  const parent = remotePath === "/" ? "/" : remotePath.slice(0, remotePath.lastIndexOf("/")) || "/";
  return cancellable({ source: "remote", connectionId: request.connectionId,
    location: encode(remotePath), parentLocation: encode(parent),
    displayPath: remotePath, entries: [], initialHome: false, ancestorLocations: [] });
}
export async function ListOperations() { return state.operations; }
export async function CancelOperation() { throw new Error("Test operation not found"); }
export async function ResolveConflict(resolution: any) {
  state.conflictResolutions.push(resolution);
  const operation = state.operations.find(value => value.operationId === resolution.operationId);
  if (!operation || operation.pendingConflict?.conflictId !== resolution.conflictId ||
    operation.pendingConflict.revision !== resolution.revision) throw new Error("Устаревший конфликт");
  operation.state = resolution.action === "cancel-operation" ? "cancelled" : "running";
  operation.pendingConflict = null;
  operation.updateVersion++;
  return operation;
}
export async function ExecuteCommand(request: any) {
  state.opens.push(request);
  if (request.type === "file.trash") {
    const source = request.context[request.context.activePanel];
    const sources = request.params.entries.map((e: any) => e.location);
    for (const path of sources) state.trashed.add(path);
    state.operations.push({ operationId: "trash-operation", type: "file.trash", state: "completed", updateVersion: 3,
      createdAt: new Date().toISOString(), finishedAt: new Date().toISOString(), error: null, pendingConflict: null,
      progress: { processedFiles: sources.length, totalFiles: sources.length, processedBytes: 0, totalBytes: null, currentFile: "" },
      context: { activePanel: request.context.activePanel, sources, sourceDirectory: source.directoryLocation,
        destinationDirectory: null, parametersJSON: JSON.stringify(request.params) } });
    return;
  }
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

function cancellable<T>(value: T) {
  const result = Promise.resolve(value) as Promise<T> & { cancel: (reason?: string) => Promise<void> };
  result.cancel = async () => {};
  return result;
}

export function OpenFile() {
  const hex = state.viewerMode === "hex";
  const image = state.viewerMode === "image";
  const name = image ? "picture without extension" : hex ? "large.bin" : "one.txt";
  return cancellable({ id: state.viewerBroken ? "viewer-broken" : "viewer-1", name, path: `${home}/${name}`,
    size: hex ? (5n * 1024n * 1024n * 1024n).toString() : image ? "68" : "4294967296",
    modifiedAt: "", initialMode: state.viewerMode, encoding: "utf-8", encodingSource: "heuristic",
    mediaType: image ? "image/png" : "" });
}
export function OpenViewer(location: string) {
  state.viewerWindows.push(location);
  return OpenFile();
}
export function OpenRemoteViewer(_connectionID: string, location: string) { return OpenViewer(location); }
export function GetViewer(id: string) {
  const source = OpenFile();
  const result = source.then(file => ({ ...file, id })) as ReturnType<typeof OpenFile>;
  result.cancel = source.cancel;
  return result;
}
export function ReadText(_id: string, offset: string, _length: number, encoding: string) {
  state.viewerReads.push(encoding);
  const actual = encoding === "auto" ? "utf-8" : encoding;
  return cancellable({ startOffset: offset, nextOffset: (BigInt(offset) + 32n).toString(),
    text: encoding === "koi8-r" ? "текст KOI8-R\n" : "UTF-8 текст\n", eof: false,
    encoding: actual, encodingSource: encoding === "auto" ? "heuristic" : "manual", auto: encoding === "auto" });
}
export function ReadFile(_id: string, offset: string, length: number) {
  const size = 5n * 1024n * 1024n * 1024n;
  const available = size - BigInt(offset);
  const actual = Math.max(0, Number(available < BigInt(length) ? available : BigInt(length)));
  state.viewerBinaryReads.push({ offset, length });
  return cancellable({ offset, dataBase64: btoa("\0".repeat(actual)), length: actual,
    eof: BigInt(offset) + BigInt(actual) >= size, size: size.toString() });
}
export function CloseFile() { state.viewerClosed++; return cancellable(undefined); }

export async function PrepareTrash(context: any) {
 state.previews.push(context);
 const panel = context[context.activePanel];
 const locations = panel.selectedLocations.length ? panel.selectedLocations : [panel.focusedLocation];
 return { directory: panel.directoryLocation, entries: locations.map((location: string) => ({
  location, name: location.split("/").at(-1), revision: "identity",
  kind: location.endsWith("/child") ? "directory" : "file",
 })) };
}
