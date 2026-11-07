import type { Directory, Entry } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/models";
import type { RowSelectionOptions } from "ag-grid-community";

// This ID cannot collide with a base64url filesystem address. It never crosses
// the backend boundary or appears in the explicit file selection.
export const PARENT_ROW_ID = "..";
export type PanelRow = Entry & { parent?: true };

export function hasParent(directory: Directory | undefined): boolean {
  return !!directory && directory.location !== directory.parentLocation;
}

export function panelRows(directory: Directory | undefined, entries: Entry[]): PanelRow[] {
  return directory && hasParent(directory) ? [{ id: PARENT_ROW_ID, name: "..", kind: "directory", parent: true,
    extension: "", size: "", modifiedAt: "", hidden: false, navigable: true, detail: "Родительский каталог" }, ...entries] : entries;
}

export function fileLocation(row: PanelRow | undefined): string | null {
  return row && !row.parent ? row.id : null;
}

export const fileSelection: RowSelectionOptions<PanelRow> = {
  mode: "multiRow", checkboxes: false, headerCheckbox: false, enableClickSelection: true,
  isRowSelectable: node => fileLocation(node.data) !== null,
};

export function entryDestination(directory: Directory | undefined, rowId: string): string | undefined {
  if (!directory) return undefined;
  if (rowId === PARENT_ROW_ID) return hasParent(directory) ? directory.parentLocation : undefined;
  return directory.entries?.find(entry => entry.id === rowId && entry.navigable)?.id;
}

// Normal upward navigation returns the cursor to the child we just left.
// AG Grid resolves this identity against its current sort/filter order.
export function returnCursor(previous: Directory | undefined, next: Directory): string | undefined {
  return previous && hasParent(previous) && previous.parentLocation === next.location ? previous.location : undefined;
}
