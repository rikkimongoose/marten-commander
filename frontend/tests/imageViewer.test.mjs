import test from "node:test";
import assert from "node:assert/strict";
import { adjacentImageZoom, imageAssetURL, imageFitScale, imageZoomLabel, imageZoomSteps } from "../src/imageViewer.ts";

test("image asset URLs target the native Wails asset server", () => {
  assert.equal(imageAssetURL("abc /?", { protocol: "wails:", host: "localhost:9245", hostname: "localhost" }),
    "wails://localhost:9245/viewer-image/abc%20%2F%3F");
  assert.equal(imageAssetURL("session", { protocol: "http:", host: "wails.localhost", hostname: "wails.localhost" }),
    "http://wails.localhost/viewer-image/session");
  assert.equal(imageAssetURL("session", { protocol: "http:", host: "127.0.0.1:9245", hostname: "127.0.0.1" }),
    "/viewer-image/session");
});

test("fit preserves aspect ratio and never unexpectedly enlarges small images", () => {
  assert.equal(imageFitScale(4000, 2000, 1000, 800), .25);
  assert.equal(imageFitScale(2000, 4000, 1000, 800), .2);
  assert.equal(imageFitScale(320, 200, 1000, 800), 1);
});

test("zoom uses bounded, predictable steps including one CSS pixel per image pixel", () => {
  assert.deepEqual(imageZoomSteps, [.25, .5, .75, 1, 1.25, 1.5, 2, 4]);
  assert.equal(adjacentImageZoom(.75, 1), 1);
  assert.equal(adjacentImageZoom(1, -1), .75);
  assert.equal(adjacentImageZoom(4, 1), 4);
  assert.equal(adjacentImageZoom(.25, -1), .25);
  assert.equal(imageZoomLabel(1.25), "125%");
});
