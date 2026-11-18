import test from "node:test";
import assert from "node:assert/strict";
import { HexLoadScheduler, hexOffsetForScroll, hexScrollTopForOffset, hexVirtualLayout, hexVisibleWindow, hexWindowForOffset,
  hexWindowSize, maximumHexVirtualHeight, needsHexWindow, parseHexOffset } from "../src/hexViewer.ts";

test("offset input accepts DEC and HEX and rejects invalid or out-of-file values", () => {
  assert.equal(parseHexOffset("4096", 8192n), 4096n);
  assert.equal(parseHexOffset(" 0x1000 ", 8192n), 4096n);
  assert.throws(() => parseHexOffset("-1", 8192n));
  assert.throws(() => parseHexOffset("0xno", 8192n));
  assert.throws(() => parseHexOffset("8192", 8192n));
  assert.throws(() => parseHexOffset("0", 0n));
});

test("a jump creates one bounded, row-aligned window around the selected byte", () => {
  const size = 10n * 1024n * 1024n;
  const window = hexWindowForOffset(1_000_007n, size);
  assert.equal(window.start % 16n, 0n);
  assert.equal(window.length, hexWindowSize);
  assert.ok(window.start < 1_000_007n && window.start + BigInt(window.length) > 1_000_007n);
  assert.equal(needsHexWindow(1_000_007n, window.start, window.length, size), false);
  assert.equal(needsHexWindow(window.start - 16n, window.start, window.length, size), true);

  const last = hexWindowForOffset(size - 1n, size);
  assert.ok(last.start + BigInt(last.length) <= size);
  assert.ok(last.length <= hexWindowSize);
});

test("virtual scrolling stays bounded and preserves offsets beyond 4 GiB", () => {
  const size = 9n * 1024n * 1024n * 1024n + 13n;
  const offset = 7n * 1024n * 1024n * 1024n + 5n;
  const window = hexWindowForOffset(offset, size);
  const layout = hexVirtualLayout(size, window.start, window.length, 720);
  assert.equal(layout.totalHeight, maximumHexVirtualHeight);
  assert.ok(layout.topHeight > 0 && layout.bottomHeight > 0);
  assert.equal(layout.topHeight + layout.contentHeight + layout.bottomHeight, layout.totalHeight);

  const top = hexScrollTopForOffset(offset, layout.totalHeight, 720, size);
  const restored = hexOffsetForScroll(top, layout.totalHeight, 720, size);
  assert.ok(restored > 6n * 1024n * 1024n * 1024n);
  assert.ok(restored < 8n * 1024n * 1024n * 1024n);
  assert.equal(restored % 16n, 0n);
});

test("a 4096-byte jump in a 5 GiB file does not collapse to the initial scroll position", () => {
  const size = 5n * 1024n * 1024n * 1024n;
  const offset = 4096n;
  const window = hexWindowForOffset(offset, size);
  const visible = hexVisibleWindow(new Uint8Array(window.length), window.start, offset, 720);
  const layout = hexVirtualLayout(size, visible.start, visible.bytes.length, 720);
  const scrollTop = hexScrollTopForOffset(offset, layout.totalHeight, 720, size);
  const restored = hexOffsetForScroll(scrollTop, layout.totalHeight, 720, size);
  assert.ok(scrollTop > 0, `4096-byte jump unexpectedly mapped to ${scrollTop}`);
  assert.equal(restored, offset);
  assert.ok(visible.start <= offset && visible.start + BigInt(visible.bytes.length) > offset);
});

test("empty and final partial rows have stable layouts", () => {
  const empty = hexVirtualLayout(0n, 0n, 0, 600);
  assert.equal(empty.totalHeight, 600);
  const final = hexVirtualLayout(17n, 0n, 17, 600);
  assert.equal(final.contentHeight, 36);
  assert.equal(final.topHeight + final.contentHeight + final.bottomHeight, final.totalHeight);
});

test("the rendered row count is bounded by the viewport, not the file or cache size", () => {
  const cache = new Uint8Array(hexWindowSize);
  const start = 4n * 1024n * 1024n * 1024n;
  const visible = hexVisibleWindow(cache, start, start + 32n * 1024n, 720);
  assert.equal(visible.rowCount, Math.ceil(720 / 18) + 16);
  assert.equal(visible.bytes.length, visible.rowCount * 16);
  assert.ok(visible.rowCount < hexWindowSize / 16);

  const outside = hexVisibleWindow(cache, start, 0n, 720);
  assert.equal(outside.rowCount, 0);
  assert.equal(outside.bytes.length, 0);
});

test("rapid scroll positions coalesce into one request for the final offset", async () => {
  const offsets = [];
  const scheduler = new HexLoadScheduler(offset => offsets.push(offset), 5);
  for (let offset = 0n; offset < 1_000n; offset++) scheduler.schedule(offset);
  await new Promise(resolve => setTimeout(resolve, 20));
  assert.deepEqual(offsets, [999n]);
  scheduler.schedule(2_000n);
  scheduler.cancel();
  await new Promise(resolve => setTimeout(resolve, 10));
  assert.deepEqual(offsets, [999n]);
});
