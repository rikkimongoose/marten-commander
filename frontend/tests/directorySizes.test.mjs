import test from "node:test";
import assert from "node:assert/strict";
import { applyDirectorySizes, DirectorySizeRequests, directorySizeTarget } from "../src/directorySizes.ts";
import { panelRows } from "../src/panelRows.ts";

const entry = (id, kind = "directory") => ({ id, name: id, kind, navigable: kind === "directory",
  extension: "", size: kind === "file" ? "3" : "", modifiedAt: "", hidden: false, detail: "" });
const directory = (source, entries) => ({ source, location: "current", parentLocation: "parent", displayPath: "/current",
  entries, ancestorLocations: [], initialHome: false });

test("Space target is the focused local directory, never selection, file, parent or SFTP", () => {
  const local = directory("local", [entry("one"), entry("file", "file")]);
  const rows = panelRows(local, local.entries);
  assert.equal(directorySizeTarget(local, rows[1]), "one");
  assert.equal(directorySizeTarget(local, rows[0]), null);
  assert.equal(directorySizeTarget(local, rows[2]), null);
  assert.equal(directorySizeTarget(directory("remote", local.entries), rows[1]), null);
});

test("concurrent results remain attached by directory identity across sorting and navigation", async () => {
  const pending = new Map();
  const statuses = {};
  const requests = new DirectorySizeRequests(location => new Promise((resolve, reject) => pending.set(location, { resolve, reject })),
    (location, status) => { statuses[location] = status; });
  requests.start("alpha");
  requests.start("beta");
  assert.deepEqual(statuses, { alpha: { state: "counting" }, beta: { state: "counting" } });

  // Simulate leaving the directory while both background calls continue.
  assert.deepEqual(applyDirectorySizes(panelRows(directory("local", [entry("other")]), [entry("other")]), statuses)
    .map(row => row.directorySize?.state), [undefined, undefined]);

  pending.get("beta").resolve({ location: "beta", size: "20" });
  pending.get("alpha").reject(new Error("permission denied"));
  await Promise.resolve(); await Promise.resolve();
  const reordered = [entry("beta"), entry("alpha")];
  const rows = applyDirectorySizes(panelRows(directory("local", reordered), reordered), statuses);
  assert.equal(rows.find(row => row.id === "beta").size, "20");
  assert.equal(rows.find(row => row.id === "beta").directorySize.state, "complete");
  assert.equal(rows.find(row => row.id === "alpha").directorySize.state, "failed");
  requests.dispose();
});

test("a stale retry cannot overwrite a newer size", async () => {
  const pending = [];
  const statuses = {};
  const requests = new DirectorySizeRequests(() => new Promise(resolve => pending.push(resolve)),
    (location, status) => { statuses[location] = status; });
  requests.start("directory");
  requests.start("directory");
  pending[1]({ location: "directory", size: "9" });
  await Promise.resolve(); await Promise.resolve();
  pending[0]({ location: "directory", size: "1" });
  await Promise.resolve(); await Promise.resolve();
  assert.deepEqual(statuses.directory, { state: "complete", size: "9" });
});
