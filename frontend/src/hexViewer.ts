export const hexBytesPerRow = 16;
export const hexRowHeight = 18;
export const hexWindowSize = 64 * 1024;
export const maximumHexVirtualHeight = 12_000_000;

export interface HexWindow {
  start: bigint;
  length: number;
}

export interface HexVirtualLayout {
  totalHeight: number;
  topHeight: number;
  contentHeight: number;
  bottomHeight: number;
}

const rowBytes = BigInt(hexBytesPerRow);

export function parseHexOffset(input: string, size: bigint): bigint {
  const value = input.trim();
  if (!/^(?:0[xX][0-9a-fA-F]+|[0-9]+)$/.test(value)) {
    throw new Error("Введите неотрицательное смещение в DEC или HEX (например, 4096 или 0x1000).");
  }
  const offset = BigInt(value);
  if (size === 0n) throw new Error("Пустой файл не содержит байтов.");
  if (offset >= size) throw new Error(`Смещение должно быть меньше размера файла (${size.toString()} байт).`);
  return offset;
}

export function hexWindowForOffset(offset: bigint, size: bigint): HexWindow {
  if (size <= 0n) return { start: 0n, length: 0 };
  const bounded = offset < 0n ? 0n : offset >= size ? size - 1n : offset;
  const rowStart = bounded / rowBytes * rowBytes;
  const buffer = BigInt(hexWindowSize / 4);
  const approximate = rowStart > buffer ? rowStart - buffer : 0n;
  const start = approximate / rowBytes * rowBytes;
  const available = size - start;
  return { start, length: Number(available < BigInt(hexWindowSize) ? available : BigInt(hexWindowSize)) };
}

export function needsHexWindow(offset: bigint, start: bigint, length: number, size: bigint): boolean {
  if (length <= 0) return size > 0n;
  const end = start + BigInt(length);
  if (offset < start || offset >= end) return true;
  const margin = BigInt(hexWindowSize / 4);
  return (start > 0n && offset < start + margin) || (end < size && offset >= end - margin);
}

export function hexVirtualLayout(size: bigint, start: bigint, byteLength: number, viewportHeight: number): HexVirtualLayout {
  const totalRows = size <= 0n ? 0n : (size + rowBytes - 1n) / rowBytes;
  const contentRows = Math.max(1, Math.ceil(byteLength / hexBytesPerRow));
  const contentHeight = contentRows * hexRowHeight;
  if (totalRows === 0n) return { totalHeight: viewportHeight, topHeight: 0, contentHeight, bottomHeight: 0 };
  const uncapped = totalRows > BigInt(Math.floor(maximumHexVirtualHeight / hexRowHeight))
    ? maximumHexVirtualHeight : Number(totalRows) * hexRowHeight;
  const totalHeight = Math.max(viewportHeight, uncapped);
  const startRow = start / rowBytes;
  const loadedRows = BigInt(contentRows);
  const maximumStartRow = totalRows > loadedRows ? totalRows - loadedRows : 0n;
  const availableHeight = Math.max(0, totalHeight - contentHeight);
  const topHeight = maximumStartRow === 0n ? 0
    : Number(startRow * 1_000_000n / maximumStartRow) / 1_000_000 * availableHeight;
  return { totalHeight, topHeight, contentHeight, bottomHeight: Math.max(0, totalHeight - topHeight - contentHeight) };
}

export function hexOffsetForScroll(scrollTop: number, scrollHeight: number, viewportHeight: number, size: bigint): bigint {
  const totalRows = size <= 0n ? 0n : (size + rowBytes - 1n) / rowBytes;
  const visibleRows = BigInt(Math.max(1, Math.ceil(viewportHeight / hexRowHeight)));
  const maximumFirstRow = totalRows > visibleRows ? totalRows - visibleRows : 0n;
  const maximumScroll = scrollHeight - viewportHeight;
  if (maximumFirstRow === 0n || maximumScroll <= 0) return 0n;
  const ratio = Math.max(0, Math.min(1, scrollTop / maximumScroll));
  return maximumFirstRow * BigInt(Math.round(ratio * 1_000_000)) / 1_000_000n * rowBytes;
}

export function hexScrollTopForOffset(offset: bigint, scrollHeight: number, viewportHeight: number, size: bigint): number {
  const totalRows = size <= 0n ? 0n : (size + rowBytes - 1n) / rowBytes;
  const visibleRows = BigInt(Math.max(1, Math.ceil(viewportHeight / hexRowHeight)));
  const maximumFirstRow = totalRows > visibleRows ? totalRows - visibleRows : 0n;
  const maximumScroll = Math.max(0, scrollHeight - viewportHeight);
  if (maximumFirstRow === 0n || maximumScroll === 0) return 0;
  const row = offset / rowBytes;
  const bounded = row > maximumFirstRow ? maximumFirstRow : row;
  return Number(bounded * 1_000_000n / maximumFirstRow) / 1_000_000 * maximumScroll;
}
