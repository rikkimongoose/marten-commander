import test from "node:test";
import assert from "node:assert/strict";
import { DirectoryLoader } from "../src/directoryLoader.ts";
import { CancellablePromise } from "@wailsio/runtime/cancellable";

const request = path => ({ path, location: "" });
const tick = () => new Promise(resolve => setImmediate(resolve));
function fixture() {
  const calls = [], results = [], errors = [];
  const loader = new DirectoryLoader(req => {
    const deferred = Promise.withResolvers();
    const call = { ...deferred, request: req, cancellations: 0 };
    // Simulate a backend that acknowledges cancellation but finishes late.
    deferred.promise.cancel = () => { call.cancellations++; };
    calls.push(call);
    return deferred.promise;
  }, { start() {}, success: value => results.push(value), failure: error => errors.push(error) });
  return { loader, calls, results, errors };
}

test("late result cannot replace a newer directory; pending requests coalesce", async () => {
  const f = fixture();
  f.loader.request(request("A"));
  f.loader.request(request("B"));
  f.loader.request(request("C"));
  assert.equal(f.calls.length, 1);
  assert.ok(f.calls[0].cancellations > 0);
  f.calls[0].resolve({ location: "A" });
  await tick();
  assert.deepEqual(f.results, []);
  assert.equal(f.calls.length, 2);
  assert.equal(f.calls[1].request.path, "C");
  f.calls[1].resolve({ location: "C" });
  await tick();
  assert.deepEqual(f.results, [{ location: "C" }]);
  f.loader.dispose();
});

test("A -> B -> A rejects the old A snapshot", async () => {
  const f = fixture();
  f.loader.request(request("A"));
  f.loader.request(request("B"));
  f.loader.request(request("A"));
  f.calls[0].resolve({ location: "A", revision: "old" });
  await tick();
  assert.equal(f.results.length, 0);
  f.calls[1].resolve({ location: "A", revision: "new" });
  await tick();
  assert.deepEqual(f.results, [{ location: "A", revision: "new" }]);
});

test("late errors are ignored and current errors are reported without a replacement snapshot", async () => {
  const f = fixture();
  f.loader.request(request("old"));
  f.loader.request(request("new"));
  f.calls[0].reject(new Error("obsolete"));
  await tick();
  assert.deepEqual(f.errors, []);
  const denied = new Error("permission denied");
  f.calls[1].reject(denied);
  await tick();
  assert.deepEqual(f.errors, [denied]);
  assert.deepEqual(f.results, []);
});

test("disposing prevents callbacks and queued requests", async () => {
  const f = fixture();
  f.loader.request(request("A"));
  f.loader.request(request("B"));
  f.loader.dispose();
  f.calls[0].resolve({ location: "A" });
  await tick();
  f.loader.request(request("C"));
  assert.equal(f.calls.length, 1);
  assert.deepEqual(f.results, []);
  assert.deepEqual(f.errors, []);
});

test("panels operate independently", async () => {
  const left = fixture(), right = fixture();
  left.loader.request(request("left"));
  right.loader.request(request("right"));
  right.calls[0].resolve({ location: "right" });
  await tick();
  assert.deepEqual(right.results, [{ location: "right" }]);
  assert.deepEqual(left.results, []);
  left.calls[0].resolve({ location: "left" });
  await tick();
  assert.deepEqual(left.results, [{ location: "left" }]);
});

test("real Wails cancellation drains the active call and starts its successor", async () => {
  const calls = [], results = [], errors = [];
  const loader = new DirectoryLoader(() => {
    const call = CancellablePromise.withResolvers();
    calls.push(call);
    return call.promise;
  }, { start() {}, success: value => results.push(value), failure: error => errors.push(error) });
  loader.request(request("old"));
  // Let await attach its rejection handler before cancelling the call.
  await tick();
  loader.request(request("new"));
  await tick();
  assert.equal(calls.length, 2);
  calls[1].resolve({ location: "new" });
  await tick();
  assert.deepEqual(results, [{ location: "new" }]);
  assert.deepEqual(errors, []);
  loader.dispose();
});

test("immediate cleanup is safe with Wails promises (React StrictMode)", async () => {
  const errors = [], results = [];
  const loader = new DirectoryLoader(() => new CancellablePromise(() => {}), {
    start() {}, success: value => results.push(value), failure: error => errors.push(error),
  });
  loader.request(request("initial"));
  loader.dispose();
  await tick();
  assert.deepEqual(results, []);
  assert.deepEqual(errors, []);
});
