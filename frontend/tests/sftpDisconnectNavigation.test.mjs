import test from "node:test";
import assert from "node:assert/strict";
import { createPanelDirectoryReader, panelNavigationRequest, restoreLocalRequest } from "../src/panelNavigation.ts";

const directory = (source, location, parentLocation, connectionId) => ({
  source, ...(connectionId ? { connectionId } : {}), location, parentLocation,
  ancestorLocations: [], initialHome: false, displayPath: location, entries: [],
});

test("local -> SFTP -> disconnect -> Enter -> Backspace keeps using local listing", async () => {
  const localCalls = [], remoteCalls = [];
  const localRoot = directory("local", "local-root", "local-parent");
  const localChild = directory("local", "local-child", "local-root");
  const remoteRoot = directory("remote", "remote-root", "remote-parent", "session-1");
  const read = request => {
    localCalls.push(request);
    if (request.location === "local-child") return Promise.resolve(localChild);
    return Promise.resolve(localRoot);
  };
  const readRemote = request => { remoteCalls.push(request); return Promise.resolve(remoteRoot); };
  const routedRead = createPanelDirectoryReader(read, () => assert.fail("volume lookup is not expected"), readRemote);

  let visible = await routedRead(panelNavigationRequest({ path: "", location: "", source: "local" }));
  const localBeforeSFTP = visible;
  visible = await routedRead(panelNavigationRequest({ path: "", location: "", source: "remote", connectionId: "session-1" }, visible));
  assert.equal(visible.source, "remote");

  // The disconnect handler restores this local request and local snapshot.
  visible = await routedRead(restoreLocalRequest(localBeforeSFTP));
  assert.equal(visible.source, "local");

  // Enter in a local subdirectory, then Backspace to its parent.
  visible = await routedRead(panelNavigationRequest({ path: "", location: "local-child" }, visible));
  assert.equal(visible, localChild);
  visible = await routedRead(panelNavigationRequest({ path: "", location: visible.parentLocation }, visible));
  assert.equal(visible, localRoot);

  assert.deepEqual(localCalls.map(call => call.location), ["", "local-root", "local-child", "local-root"]);
  assert.equal(remoteCalls.length, 1);
  assert.equal(remoteCalls[0].connectionId, "session-1");
});
