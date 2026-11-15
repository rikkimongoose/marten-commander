import test from "node:test";
import assert from "node:assert/strict";
import { moveFocusCandidates, operationRefreshLocations } from "../src/operationRefresh.ts";

const operation = (type, source, destination, entered = "/target") => ({ type, context: {
  sourceDirectory: source, destinationDirectory: destination, parametersJSON: JSON.stringify({ destination: entered }),
  sources: [Buffer.from("/source/old.txt").toString("base64url")],
} });

test("completed and partial moves refresh their captured source and destination panels", () => {
  const entered = Buffer.from("/target").toString("base64url");
  assert.deepEqual(operationRefreshLocations(operation("file.move", "source", "destination")), ["destination", entered, "source"]);
  assert.deepEqual(operationRefreshLocations(operation("file.move", "same", "same", "same")), ["same"]);
});

test("single move restores cursor for either a renamed target or an existing destination directory", () => {
  const value = operation("file.move", "source", Buffer.from("/target").toString("base64url"), "/target/новое.txt");
  assert.deepEqual(moveFocusCandidates(value), [
    { directoryLocation: Buffer.from("/target").toString("base64url"), name: "новое.txt" },
    { directoryLocation: Buffer.from("/target/новое.txt").toString("base64url"), name: "old.txt" },
  ]);
  value.context.sources.push(Buffer.from("/source/second").toString("base64url"));
  assert.deepEqual(moveFocusCandidates(value), []);
});

test("copy refreshes only destination and unrelated operations refresh nothing", () => {
  assert.deepEqual(operationRefreshLocations(operation("file.copy", "source", "destination")), ["destination"]);
  assert.deepEqual(operationRefreshLocations(operation("file.mkdir", "source", "destination")), []);
});
