import type { PanelRow } from "./panelRows.ts";

const hexColor = /^#[0-9A-Fa-f]{6}$/;

// Always return a colour, including for unstyled rows. AG Grid reuses DOM
// cells while scrolling, so an explicit "inherit" clears an earlier inline
// file colour instead of leaking it into the next row.
export function fileNameCellStyle(row: PanelRow | undefined): { color: string } {
  const color = row && !row.parent && row.kind === "file" && row.color && hexColor.test(row.color) ? row.color : "inherit";
  return { color };
}
