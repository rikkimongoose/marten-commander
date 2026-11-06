import test from "node:test";
import assert from "node:assert/strict";
import { CommandDispatcher } from "../src/commands.ts";
import { DirectoryLoader } from "../src/directoryLoader.ts";
import { registerLocationCommand } from "../src/locationCommand.ts";
import { createPanelReader, selectedHomeAfterNavigation } from "../src/panelNavigation.ts";
import { formatBytes, loadVolumeChoices, locationCaption, spacePresentation } from "../src/volumePresentation.ts";
import { CancellablePromise } from "@wailsio/runtime/cancellable";

const tick = () => new Promise(resolve => setImmediate(resolve));
const panel = () => ({ directoryLocation: "initial", selectedLocations: [], focusedLocation: null });
const context = () => ({ activePanel: "left", left: panel(), right: panel() });
const volume = (id, location) => ({ id, rootLocation: location, name: id, displayPath: location, space: null, warning: "" });

test("space formatting preserves large integers and distinguishes available from physically used", () => {
  const result = spacePresentation({ total: "1000", free: "400", available: "300", used: "600" });
  assert.equal(result.percent, 60);
  assert.match(result.text, /300 Б свободно из 1\s000 Б/);
  assert.match(result.title, /100 Б/);
  assert.equal(formatBytes(1024n), "1 КиБ");
  assert.equal(formatBytes(1536n), "1,5 КиБ");
  assert.match(formatBytes(18446744073709551615n), /ЭиБ$/);
  assert.equal(spacePresentation({ total: "9007199254740993", free: "0", available: "0", used: "9007199254740993" }).percent, 100);
});

test("unknown, zero and invalid capacity stay neutral without fabricated counters", () => {
  for (const value of [undefined, null, { total: "0", free: "0", available: "0", used: "0" },
    { total: "100", free: "150", available: "20", used: "0" }, { total: "100", free: "40", available: "50", used: "60" },
    { total: "100", free: "40", available: "30", used: "61" }, { total: "bad", free: "0", available: "0", used: "0" }]) {
    assert.equal(spacePresentation(value).percent, null);
    assert.equal(spacePresentation(value).text, "Объём недоступен");
  }
});

test("Home stays separate even when it shares the root volume; list is read anew on each open", async () => {
  const home = { location: "home", displayPath: "/home/test" };
  let calls = 0;
  const list = async () => { calls++; return [volume("root", "/")]; };
  for (let i = 0; i < 2; i++) {
    const result = await loadVolumeChoices(list, async () => home);
    assert.deepEqual(result.home, home);
    assert.deepEqual(result.volumes, [volume("root", "/")]);
    assert.deepEqual(result.errors, []);
  }
  assert.equal(calls, 2);
});

test("Home remains available on enumeration failure and volumes remain available on Home failure", async () => {
  const fail = async () => { throw new Error("unavailable"); };
  let result = await loadVolumeChoices(fail, async () => ({ location: "home", displayPath: "/home" }));
  assert.equal(result.home.location, "home"); assert.deepEqual(result.volumes, []); assert.deepEqual(result.errors, ["unavailable"]);
  result = await loadVolumeChoices(async () => [volume("disk", "/disk")], fail);
  assert.equal(result.home, null); assert.equal(result.volumes.length, 1); assert.deepEqual(result.errors, ["unavailable"]);
});

test("Home is published before a slow device enumeration finishes", async () => {
  const slow = Promise.withResolvers();
  const updates = [];
  const home = { location: "home", displayPath: "/home/test" };
  const pending = loadVolumeChoices(() => slow.promise, async () => home, result => updates.push(result));
  await tick();
  assert.equal(updates.length, 1);
  assert.deepEqual(updates[0].home, home);
  assert.equal(updates[0].loadingHome, false);
  assert.equal(updates[0].loadingVolumes, true);
  slow.resolve([volume("disk", "/disk")]);
  const result = await pending;
  assert.equal(result.loadingVolumes, false);
  assert.equal(result.volumes.length, 1);
  assert.deepEqual(result.home, home);
  assert.deepEqual(updates[0].volumes, [], "previous published snapshots stay unchanged");
});

test("volume choices are usable before Home finishes or fails", async () => {
  const slow = Promise.withResolvers();
  const updates = [];
  const pending = loadVolumeChoices(async () => [volume("disk", "/disk")], () => slow.promise, result => updates.push(result));
  await tick();
  assert.equal(updates[0].volumes.length, 1);
  assert.equal(updates[0].loadingHome, true);
  assert.equal(updates[0].loadingVolumes, false);
  slow.reject(new Error("Home unavailable"));
  const result = await pending;
  assert.equal(result.loadingHome, false);
  assert.deepEqual(result.errors, ["Home unavailable"]);
  assert.equal(result.volumes.length, 1);
});

test("closing the menu cancels both actual Wails calls without publishing errors", { timeout: 2000 }, async () => {
  const controller = new AbortController();
  const devices = CancellablePromise.withResolvers();
  const home = CancellablePromise.withResolvers();
  const updates = [];
  const pending = loadVolumeChoices(() => devices.promise, () => home.promise, result => updates.push(result), controller.signal);
  controller.abort(); // Also exercises immediate cleanup in React StrictMode.
  await pending;
  assert.deepEqual(updates, []);
  const outcomes = await Promise.allSettled([devices.promise, home.promise]);
  assert.ok(outcomes.every(result => result.status === "rejected" && result.reason.name === "CancelError"));
});

test("late uncancellable replies from a closed menu cannot update its replacement", async () => {
  const controller = new AbortController();
  const devices = Promise.withResolvers(), home = Promise.withResolvers();
  const updates = [];
  const old = loadVolumeChoices(() => devices.promise, () => home.promise, result => updates.push(result), controller.signal);
  controller.abort();
  const latest = await loadVolumeChoices(async () => [volume("new", "/new")], async () => ({ location: "new-home", displayPath: "/new-home" }), result => updates.push(result));
  const updateCount = updates.length;
  devices.resolve([volume("old", "/old")]);
  home.reject(new Error("obsolete"));
  await old;
  assert.equal(updates.length, updateCount);
  assert.deepEqual(updates.at(-1), latest);
  assert.equal(latest.home.location, "new-home");
});

test("already closed menus do not issue OS calls", async () => {
  const controller = new AbortController();
  controller.abort();
  const unexpected = () => assert.fail("a closed menu must not start a call");
  const result = await loadVolumeChoices(unexpected, unexpected, unexpected, controller.signal);
  assert.equal(result.loadingHome, false);
  assert.equal(result.loadingVolumes, false);
});

test("location command uses captured panel and forwards immutable invocation parameters", async () => {
  const state = context();
  const received = [];
  const dispatcher = new CommandDispatcher(() => state);
  registerLocationCommand(dispatcher, (panel, location, id) => received.push({ panel, location, id }));
  const params = { location: "left-root", volumeId: "left-disk" };
  const first = dispatcher.dispatch("panel.goToLocation", params);
  state.activePanel = "right"; params.location = "changed";
  await first;
  await dispatcher.dispatch("panel.goToLocation", { location: "home" });
  assert.deepEqual(received, [{ panel: "left", location: "left-root", id: "left-disk" }, { panel: "right", location: "home", id: undefined }]);
  await assert.rejects(dispatcher.dispatch("panel.goToLocation", {}), error => error.code === "invalid_params");
  assert.equal(received.length, 2);
});

test("dispatcher freezes initial params before asynchronous preparation", async () => {
  const gate = Promise.withResolvers();
  const params = { location: "root", nested: { name: "original" } };
  let prepared, received;
  const dispatcher = new CommandDispatcher(context);
  dispatcher.register("test.prepare", { available: () => ({ enabled: true }),
    prepare: async (_context, input) => { prepared = input; await gate.promise; return input; }, execute: request => { received = request; } });
  const call = dispatcher.dispatch("test.prepare", params);
  params.nested.name = "changed"; gate.resolve(); await call;
  assert.equal(prepared.nested.name, "original");
  assert.equal(received.params.nested.name, "original");
  assert.throws(() => { prepared.nested.name = "mutation"; }, TypeError);
});

test("Home uses ordinary directory navigation, without a fictional volume lookup", async () => {
  const calls = [];
  const reader = createPanelReader(async request => { calls.push(request); return { location: request.location }; }, () => assert.fail("Home is not a volume"));
  assert.deepEqual(await reader({ path: "", location: "home", home: true }), { location: "home" });
  assert.deepEqual(calls, [{ path: "", location: "home" }]);
});

const directory = (location, ancestorLocations = [], displayPath = location) => ({
  location, ancestorLocations, displayPath, entries: [{ id: `${location}-file`, name: "retained.txt" }],
});

test("both panels initially select Home from the backend default, independently of the System volume", async () => {
  const home = { ...directory("resolved-home", ["root"], "/home/test"), initialHome: true };
  const system = { ...volume("root", "/"), name: "System" };
  const selected = {};
  const loaders = Object.fromEntries(["left", "right"].map(id => [id, new DirectoryLoader(
    async request => request.location ? directory(request.location, []) : home,
    { start() {}, success: (next, request) => { selected[id] = selectedHomeAfterNavigation(selected[id], next, request); }, failure: assert.fail },
  )]));
  for (const loader of Object.values(loaders)) loader.request({ path: "", location: "" });
  await tick();
  assert.equal(locationCaption(selected.left, system), "Home — /home/test");
  assert.equal(locationCaption(selected.right, system), "Home — /home/test");
  loaders.left.request({ path: "", location: "root" }); await tick();
  assert.equal(locationCaption(selected.left, system), "System — /");
  assert.equal(locationCaption(selected.right, system), "Home — /home/test");
  for (const loader of Object.values(loaders)) loader.dispose();
});

test("a working-directory fallback and ordinary navigation do not implicitly select Home", () => {
  const fallback = { ...directory("cwd", ["root"], "/work"), initialHome: false };
  assert.equal(selectedHomeAfterNavigation(undefined, fallback, { path: "", location: "" }), undefined);
  const home = { ...directory("home", ["root"], "/home/test"), initialHome: true };
  for (const request of [{ path: "", location: "home" }, { path: "/home/test", location: "" }]) {
    assert.equal(selectedHomeAfterNavigation(undefined, home, request), undefined);
  }
});

test("explicit Home stays selected on System, descendants and refresh; capacity uses the real filesystem", () => {
  const homeDirectory = directory("home-id", ["users-id", "root-id"], "/home/test");
  let selected = selectedHomeAfterNavigation(undefined, homeDirectory, { home: true });
  const system = { ...volume("system", "/"), name: "System", space: { total: "1000", free: "400", available: "300", used: "600" } };
  assert.equal(locationCaption(selected, system), "Home — /home/test");
  assert.equal(locationCaption(selected, undefined), "Home — /home/test", "capacity pending/failed must not hide Home");
  selected = selectedHomeAfterNavigation(selected, directory("child-id", ["home-id", "users-id", "root-id"]), {});
  selected = selectedHomeAfterNavigation(selected, directory("child-id", ["home-id", "users-id", "root-id"]), {});
  assert.equal(locationCaption(selected, system), "Home — /home/test");
  assert.equal(spacePresentation(system.space).percent, 60);
  assert.match(spacePresentation(system.space).text, /300 Б свободно из 1\s000 Б/);
  const nestedMount = { ...volume("data", "/home/test/data"), space: { total: "100", free: "20", available: "20", used: "80" } };
  assert.equal(locationCaption(selected, nestedMount), "Home — /home/test");
  assert.equal(spacePresentation(nestedMount.space).percent, 80, "Home may contain another mounted filesystem");
  selected = selectedHomeAfterNavigation(selected, homeDirectory, {});
  assert.equal(selected.location, "home-id");
  selected = selectedHomeAfterNavigation(selected, directory("users-id", ["root-id"]), {});
  assert.equal(locationCaption(selected, system), "System — /");
  assert.equal(selectedHomeAfterNavigation(selected, homeDirectory, {}), undefined, "ordinary re-entry does not select Home implicitly");
});

test("Home uses canonical ancestry, not display-path prefixes, and an explicit volume choice clears it", () => {
  const homeDirectory = directory("resolved-home", ["root"], "/home/test");
  const selected = selectedHomeAfterNavigation(undefined, homeDirectory, { home: true, location: "home-symlink" });
  assert.equal(selected.location, "resolved-home");
  for (const next of [directory("sibling", ["root"], "/home/test-other"), directory("external-link-target", ["root"], "/outside")]) {
    assert.equal(selectedHomeAfterNavigation(selected, next, {}), undefined);
  }
  assert.equal(selectedHomeAfterNavigation(selected, homeDirectory, { volumeId: "home-mount" }), undefined);
});

test("Home selection follows the captured panel and only the latest successful navigation", async () => {
  const state = context();
  const slow = Promise.withResolvers();
  const snapshots = {}, selected = {}, errors = [];
  const loaders = Object.fromEntries(["left", "right"].map(id => [id, new DirectoryLoader(request =>
    request.location === "slow-home" ? slow.promise : Promise.resolve(directory(request.location, ["root"])), {
    start() {},
    success: (next, request) => { snapshots[id] = next; selected[id] = selectedHomeAfterNavigation(selected[id], next, request); },
    failure: error => errors.push(error),
  })]));
  const dispatcher = new CommandDispatcher(() => state);
  registerLocationCommand(dispatcher, (id, location, volumeId, home) => loaders[id].request({ path: "", location, volumeId, home }));
  const params = { location: "home", home: true };
  const pending = dispatcher.dispatch("panel.goToLocation", params);
  state.activePanel = "right"; params.home = false;
  await pending; await tick();
  assert.equal(selected.left.location, "home");
  assert.equal(selected.right, undefined);
  await dispatcher.dispatch("panel.goToLocation", { location: "right-home", home: true }); await tick();
  assert.equal(selected.right.location, "right-home");
  state.activePanel = "left";
  await dispatcher.dispatch("panel.goToLocation", { location: "slow-home", home: true });
  loaders.left.request({ path: "", location: "outside" });
  slow.resolve(directory("late-home", ["root"])); await tick();
  assert.equal(snapshots.left.location, "outside");
  assert.equal(selected.left, undefined, "obsolete Home must not overwrite a later navigation");
  assert.equal(selected.right.location, "right-home");
  assert.deepEqual(errors, []);
  for (const params of [{ location: "home", home: "yes" }, { location: "home", home: true, volumeId: "system" }]) {
    await assert.rejects(dispatcher.dispatch("panel.goToLocation", params), error => error.code === "invalid_params");
  }
  loaders.left.dispose(); loaders.right.dispose();
});

test("Permission denied for /boot/efi preserves the directory, files and Home selection and allows recovery", async () => {
  let snapshot, selected, error, loading;
  const reader = createPanelReader(async request => {
    if (request.location === "efi" || request.location === "denied-home") throw new Error("Нет прав на чтение каталога /boot/efi");
    return directory(request.location, request.location === "child" ? ["home", "root"] : ["root"]);
  }, async request => volume("efi-volume", request.location));
  const loader = new DirectoryLoader(reader, {
    start() { loading = true; error = undefined; },
    success(next, request) { snapshot = next; selected = selectedHomeAfterNavigation(selected, next, request); loading = false; },
    failure(reason) { error = reason.message; loading = false; },
  });
  loader.request({ path: "", location: "home", home: true }); await tick();
  const previous = snapshot, previousSelection = selected;
  for (const request of [{ location: "efi" }, { location: "efi", volumeId: "efi-volume" }, { location: "denied-home", home: true }]) {
    loader.request({ path: "", ...request }); await tick();
    assert.equal(snapshot, previous, "keep the entire previous snapshot, including file rows");
    assert.equal(selected, previousSelection);
    assert.equal(error, "Нет прав на чтение каталога /boot/efi");
    assert.equal(loading, false);
  }
  loader.request({ path: "", location: "child" }); await tick();
  assert.equal(snapshot.location, "child");
  assert.equal(selected.location, "home");
  assert.equal(error, undefined);
  assert.equal(loading, false);
  loader.dispose();
});

test("volume validation allows unknown capacity but rejects disconnected/replaced volumes before navigating", async () => {
  let reads = 0;
  const read = async () => { reads++; return { location: "root" }; };
  const request = { path: "", location: "root", volumeId: "disk" };
  const good = createPanelReader(read, async () => volume("disk", "root"));
  await good(request); assert.equal(reads, 1);
  for (const response of [volume("other", "root"), volume("disk", "elsewhere")]) {
    await assert.rejects(createPanelReader(read, async () => response)(request), error => error.code === "unavailable");
  }
  await assert.rejects(createPanelReader(read, async () => { throw new Error("not accessible"); })(request), /not accessible/);
  assert.equal(reads, 1);
});

test("panels load independently and a later ordinary navigation supersedes pending volume validation", async () => {
  const check = Promise.withResolvers();
  const reads = [], results = { left: [], right: [] }, errors = [];
  const reader = createPanelReader(async request => { reads.push(request.location); return { location: request.location }; }, () => check.promise);
  const loaders = Object.fromEntries(["left", "right"].map(id => [id, new DirectoryLoader(reader, {
    start() {}, success: directory => results[id].push(directory.location), failure: error => errors.push(error),
  })]));
  const state = context();
  const dispatcher = new CommandDispatcher(() => state);
  registerLocationCommand(dispatcher, (id, location, volumeId) => loaders[id].request({ path: "", location, volumeId }));
  await dispatcher.dispatch("panel.goToLocation", { location: "old", volumeId: "disk" });
  state.activePanel = "right";
  await dispatcher.dispatch("panel.goToLocation", { location: "home" });
  await tick();
  assert.deepEqual(results, { left: [], right: ["home"] });
  loaders.left.request({ path: "", location: "new-manual-path" });
  check.resolve(volume("disk", "old")); await tick();
  assert.deepEqual(reads, ["home", "new-manual-path"]);
  assert.deepEqual(results, { left: ["new-manual-path"], right: ["home"] });
  assert.deepEqual(errors, []);
  loaders.left.dispose(); loaders.right.dispose();
});

test("an inaccessible volume leaves the last successful snapshot in place", async () => {
  const results = [], errors = [];
  const reader = createPanelReader(async request => ({ location: request.location }), async () => { throw new Error("disconnected"); });
  const loader = new DirectoryLoader(reader, { start() {}, success: value => results.push(value.location), failure: error => errors.push(error.message) });
  loader.request({ path: "", location: "initial" }); await tick();
  loader.request({ path: "", location: "gone", volumeId: "disk" }); await tick();
  assert.deepEqual(results, ["initial"]); assert.deepEqual(errors, ["disconnected"]);
});

test("volume cancellation reaches the lookup and the directory read stages", { timeout: 2000 }, async () => {
  const lookup = CancellablePromise.withResolvers();
  let reads = 0;
  const reader = createPanelReader(() => { reads++; return Promise.resolve({ location: "root" }); }, () => lookup.promise);
  const pendingLookup = reader({ path: "", location: "root", volumeId: "disk" });
  const lookupResult = assert.rejects(pendingLookup, error => error.name === "CancelError");
  await pendingLookup.cancel();
  await lookupResult;
  assert.equal(reads, 0);

  const directory = CancellablePromise.withResolvers();
  const afterLookup = createPanelReader(() => { reads++; return directory.promise; }, async () => volume("disk", "root"));
  const pendingRead = afterLookup({ path: "", location: "root", volumeId: "disk" });
  const readResult = assert.rejects(pendingRead, error => error.name === "CancelError");
  await tick();
  assert.equal(reads, 1);
  await pendingRead.cancel();
  await readResult;
});

test("unmount between validation and reading keeps the previous snapshot", async () => {
  const results = [], errors = [];
  const reader = createPanelReader(async request => {
    if (request.location === "gone") throw new Error("device disappeared while reading");
    return { location: request.location };
  }, async request => volume("disk", request.location));
  const loader = new DirectoryLoader(reader, { start() {}, success: value => results.push(value.location), failure: error => errors.push(error.message) });
  loader.request({ path: "", location: "initial" }); await tick();
  loader.request({ path: "", location: "gone", volumeId: "disk" }); await tick();
  assert.deepEqual(results, ["initial"]);
  assert.deepEqual(errors, ["device disappeared while reading"]);
  loader.dispose();
});
