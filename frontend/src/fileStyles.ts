import type { PanelRow } from "./panelRows.ts";

const hexColor = /^#[0-9A-Fa-f]{6}$/;
const selectedNameColor = "--marten-selected-name-color";

// Always return a colour, including for unstyled rows. AG Grid reuses DOM
// cells while scrolling, so an explicit "inherit" clears an earlier inline
// file colour instead of leaking it into the next row. Explicit selection
// defines the custom property on the row; its normal selected-text colour then
// wins without discarding the configured fallback after deselection.
export function fileNameCellStyle(row: PanelRow | undefined): { color: string } {
  const color = row && !row.parent && row.kind === "file" && row.color && hexColor.test(row.color) ? row.color : "inherit";
  return { color: `var(${selectedNameColor}, ${color})` };
}
