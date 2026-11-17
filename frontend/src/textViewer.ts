export type TextEncoding = "auto" | "utf-8" | "utf-16le" | "utf-16be" | "windows-1251" | "koi8-r";

export const textEncodings: readonly { id: TextEncoding; label: string }[] = Object.freeze([
  { id: "auto", label: "Auto" },
  { id: "utf-8", label: "UTF-8" },
  { id: "utf-16le", label: "UTF-16 LE" },
  { id: "utf-16be", label: "UTF-16 BE" },
  { id: "windows-1251", label: "Windows-1251" },
  { id: "koi8-r", label: "KOI8-R" },
]);

export const textWindowSize = 128 * 1024;
export const maximumVirtualHeight = 12_000_000;

export function encodingLabel(value: string): string {
  return textEncodings.find(item => item.id === value)?.label ?? value;
}

export function encodingStatus(auto: boolean, encoding: string, source: string): string {
  const origin = source === "bom" ? "BOM" : source === "heuristic" ? "проверка UTF-8"
    : source === "default" ? "fallback по умолчанию" : "ручной выбор";
  return `${auto ? "Auto" : "Вручную"} · ${encodingLabel(encoding)} · ${origin}`;
}

export interface VirtualWindowLayout {
  totalHeight: number;
  topHeight: number;
  contentHeight: number;
  bottomHeight: number;
}

// Byte-proportional navigation is deliberately approximate: it supports huge
// files without a full line index and keeps the same byte position when the
// encoding changes. Only the small decoded working window is rendered.
export function virtualWindowLayout(size: bigint, start: bigint, end: bigint, text: string, viewportHeight: number): VirtualWindowLayout {
  const lines = Math.max(1, text.split("\n").length);
  const contentHeight = Math.max(18, lines * 18);
  if (size <= 0n) return { totalHeight: viewportHeight, topHeight: 0, contentHeight, bottomHeight: 0 };
  const estimatedLines = Math.max(lines, Number(size > 100_000_000n ? 1_250_000n : size / 80n));
  const totalHeight = Math.max(viewportHeight, Math.min(maximumVirtualHeight, estimatedLines * 18));
  const ratio = Number((start * 1_000_000n) / size) / 1_000_000;
  const topHeight = Math.max(0, Math.min(totalHeight - contentHeight, ratio * totalHeight));
  return { totalHeight, topHeight, contentHeight, bottomHeight: Math.max(0, totalHeight - topHeight - contentHeight) };
}

export function byteOffsetForScroll(scrollTop: number, scrollHeight: number, viewportHeight: number, size: bigint): bigint {
  if (size <= 0n || scrollHeight <= viewportHeight) return 0n;
  const ratio = Math.max(0, Math.min(1, scrollTop / (scrollHeight - viewportHeight)));
  const millionths = BigInt(Math.round(ratio * 1_000_000));
  const centre = size * millionths / 1_000_000n;
  return centre > BigInt(textWindowSize / 2) ? centre - BigInt(textWindowSize / 2) : 0n;
}

export function needsTextWindow(target: bigint, start: bigint, end: bigint): boolean {
  const margin = BigInt(textWindowSize / 4);
  return target + margin < start || target + margin > end;
}

export class RequestGeneration {
  #value = 0;
  begin(): number { return ++this.#value; }
  invalidate(): void { this.#value++; }
  current(value: number): boolean { return value === this.#value; }
}
