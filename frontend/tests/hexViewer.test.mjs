import test from "node:test";
import assert from "node:assert/strict";
import { hexOffsetForScroll, hexScrollTopForOffset, hexVirtualLayout, hexWindowForOffset,
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

test("empty and final partial rows have stable layouts", () => {
  const empty = hexVirtualLayout(0n, 0n, 0, 600);
  assert.equal(empty.totalHeight, 600);
  const final = hexVirtualLayout(17n, 0n, 17, 600);
  assert.equal(final.contentHeight, 36);
  assert.equal(final.topHeight + final.contentHeight + final.bottomHeight, final.totalHeight);
});
