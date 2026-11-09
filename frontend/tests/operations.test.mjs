import test from "node:test";
import assert from "node:assert/strict";
import { connectOperationUpdates } from "../src/operationClient.ts";
import { applyOperationEvent, formatBytes, mergeOperations, operationIsCancellable, operationProgress } from "../src/operationState.ts";

function operation(id, state = "running", version = 1, progress = {}) {
  return { operationId: id, type: "copy", state, updateVersion: version, createdAt: `2026-01-01T00:00:0${id}.000Z`,
    finishedAt: null, error: null, pendingConflict: null, progress: { processedFiles: 0, totalFiles: null, processedBytes: 0,
      totalBytes: null, currentFile: "", ...progress } };
}

test("operation events update state and stale events are ignored by version", () => {
  const running = operation("1", "running", 3);
  const completed = operation("1", "completed", 4);
  let state = mergeOperations([], [running]);
  state = applyOperationEvent(state, { operationId: "1", state: "completed", progress: completed.progress,
    updateVersion: 4, operation: completed });
  assert.equal(state[0].state, "completed");
  state = applyOperationEvent(state, { operationId: "1", state: "running", progress: running.progress,
    updateVersion: 3, operation: running });
  assert.equal(state[0].state, "completed");
  assert.equal(applyOperationEvent(state, { operationId: "wrong", updateVersion: 4, operation: completed }), state);
});

test("API restoration and events coexist when an older list response arrives late", async () => {
  const request = Promise.withResolvers();
  let listener, unsubscribed = false, state = [];
  const disconnect = connectOperationUpdates({ list: () => request.promise, cancel: async () => operation("1", "cancelled", 5) },
    receive => { listener = receive; return () => { unsubscribed = true; }; },
    list => { state = mergeOperations(state, list); },
    event => { state = applyOperationEvent(state, event); },
    error => assert.fail(error));
  const completed = operation("1", "completed", 4);
  listener({ operationId: "1", state: "completed", progress: completed.progress, updateVersion: 4, operation: completed });
  request.resolve([operation("1", "running", 2), operation("2", "queued", 1)]);
  await Promise.resolve();
  await Promise.resolve();
  assert.deepEqual(state.map(value => [value.operationId, value.state]), [["2", "queued"], ["1", "completed"]]);
  disconnect();
  assert.equal(unsubscribed, true);
});

test("disconnect ignores a pending API response and later events", async () => {
  const request = Promise.withResolvers();
  let listener, calls = 0;
  const disconnect = connectOperationUpdates({ list: () => request.promise, cancel: async () => operation("1") },
    receive => { listener = receive; return () => {}; }, () => { calls++; }, () => { calls++; }, () => { calls++; });
  disconnect();
  request.resolve([operation("1")]);
  listener({ operationId: "1", updateVersion: 1, operation: operation("1") });
  await Promise.resolve();
  assert.equal(calls, 0);
});

test("unknown totals use indeterminate progress while known totals use a percentage", () => {
  assert.deepEqual(operationProgress(operation("1", "running", 1, { processedBytes: 50 })), { determinate: false, value: 0 });
  assert.deepEqual(operationProgress(operation("1", "running", 1, { processedBytes: 50, totalBytes: 200 })), { determinate: true, value: 25 });
  assert.deepEqual(operationProgress(operation("1", "running", 1, { processedFiles: 3, totalFiles: 4 })), { determinate: true, value: 75 });
  assert.deepEqual(operationProgress(operation("1", "completed", 2)), { determinate: true, value: 100 });
});

test("only live operation states are cancellable and byte values are readable", () => {
  for (const state of ["queued", "running", "waiting-for-user"]) assert.equal(operationIsCancellable(state), true);
  for (const state of ["completed", "failed", "cancelled"]) assert.equal(operationIsCancellable(state), false);
  assert.equal(formatBytes(0), "0 Б");
  assert.equal(formatBytes(1536), "1.50 КБ");
});

test("a conflict event remains recoverable from a newer backend snapshot", async () => {
  const request = Promise.withResolvers();
  let listener, state = [];
  connectOperationUpdates({ list: () => request.promise, cancel: async () => operation("1"), resolveConflict: async () => operation("1") },
    receive => { listener = receive; return () => {}; },
    list => { state = mergeOperations(state, list); }, event => { state = applyOperationEvent(state, event); }, error => assert.fail(error));
  const waiting = { ...operation("1", "waiting-for-user", 3), pendingConflict: { operationId: "1", conflictId: "c1",
    revision: 3, type: "file/file", source: { path: "/source", kind: "file", size: 1, modifiedAt: null },
    destination: { path: "/target", kind: "file", size: 2, modifiedAt: null },
    allowedActions: ["replace", "skip", "keep-both", "cancel-operation"] } };
  listener({ operationId: "1", state: waiting.state, progress: waiting.progress, updateVersion: 3, operation: waiting });
  request.resolve([waiting]);
  await Promise.resolve(); await Promise.resolve();
  assert.equal(state[0].pendingConflict.conflictId, "c1");
  assert.equal(state[0].state, "waiting-for-user");
});
