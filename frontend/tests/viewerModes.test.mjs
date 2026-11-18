import test from "node:test";
import assert from "node:assert/strict";
import { decodeBase64, hexPreview, normalizeViewerMode, textPreview, viewerEscapeAction, viewerModes } from "../src/viewerModes.ts";

test("viewer exposes working text, hex and image modes", () => {
  assert.deepEqual(viewerModes.map(mode => [mode.id, mode.available]), [
    ["text", true], ["hex", true], ["image", true],
  ]);
  assert.equal(normalizeViewerMode("text"), "text");
  assert.equal(normalizeViewerMode("hex"), "hex");
  assert.equal(normalizeViewerMode("image"), "image");
  assert.equal(normalizeViewerMode("something-new"), "unknown");
});

test("Escape closes an open menu first and then the Viewer", () => {
  assert.equal(viewerEscapeAction("Enter", false), "none");
  assert.equal(viewerEscapeAction("Escape", true), "closeMenu");
  assert.equal(viewerEscapeAction("Escape", false), "closeViewer");
});

test("viewer decodes bounded binary chunks explicitly for text and hex renderers", () => {
  const bytes = decodeBase64(Buffer.from([0x41, 0, 0xff]).toString("base64"));
  assert.deepEqual([...bytes], [0x41, 0, 0xff]);
  assert.match(textPreview(bytes), /^A/);
  assert.equal(hexPreview(bytes), "00000000  41 00 ff                                         |A..|");
  assert.match(hexPreview(Uint8Array.from([1]), 0x80000000n), /^80000000/);
});
