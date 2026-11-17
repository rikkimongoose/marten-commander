import test from "node:test";
import assert from "node:assert/strict";
import { byteOffsetForScroll, encodingStatus, maximumVirtualHeight, needsTextWindow, RequestGeneration,
  textEncodings, textWindowSize, virtualWindowLayout } from "../src/textViewer.ts";

test("encoding picker exposes Auto and every required manual encoding", () => {
  assert.deepEqual(textEncodings.map(item => item.id), ["auto", "utf-8", "utf-16le", "utf-16be", "windows-1251", "koi8-r"]);
  assert.equal(encodingStatus(true, "utf-8", "bom"), "Auto · UTF-8 · BOM");
  assert.equal(encodingStatus(true, "windows-1251", "default"), "Auto · Windows-1251 · fallback по умолчанию");
  assert.equal(encodingStatus(false, "koi8-r", "manual"), "Вручную · KOI8-R · ручной выбор");
});

test("request generations reject stale async responses and invalidation covers close", () => {
  const generation = new RequestGeneration();
  const auto = generation.begin();
  const manual = generation.begin();
  assert.equal(generation.current(auto), false);
  assert.equal(generation.current(manual), true);
  generation.invalidate();
  assert.equal(generation.current(manual), false);
});

test("large-file navigation maps a bounded virtual scrollbar to byte windows", () => {
  const size = 4n * 1024n * 1024n * 1024n;
  const layout = virtualWindowLayout(size, 2n * 1024n * 1024n * 1024n,
    2n * 1024n * 1024n * 1024n + BigInt(textWindowSize), "line\nline", 800);
  assert.equal(layout.totalHeight, maximumVirtualHeight);
  assert.ok(layout.topHeight > maximumVirtualHeight * .49 && layout.topHeight < maximumVirtualHeight * .51);
  assert.ok(layout.bottomHeight > 0);
  const target = byteOffsetForScroll(5_000, 10_800, 800, size);
  assert.ok(target > size * 4n / 10n && target < size * 6n / 10n);
});

test("virtualization retains only a small window and avoids reloads inside it", () => {
  const start = 1_000_000n, end = start + BigInt(textWindowSize);
  assert.equal(needsTextWindow(start + BigInt(textWindowSize / 2), start, end), false);
  assert.equal(needsTextWindow(0n, start, end), true);
  assert.equal(needsTextWindow(end + BigInt(textWindowSize), start, end), true);
  const empty = virtualWindowLayout(0n, 0n, 0n, "", 600);
  assert.equal(empty.totalHeight, 600);
});
