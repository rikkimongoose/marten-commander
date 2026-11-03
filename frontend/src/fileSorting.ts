import type { Entry } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/models";

export type SortField = "name" | "extension" | "size" | "modifiedAt";
const collator = new Intl.Collator("ru", { numeric: true, sensitivity: "base" });
const compare = (a: string | bigint, b: string | bigint) => a < b ? -1 : a > b ? 1 : 0;

// AG Grid applies the final descending inversion. Compensate ONLY for grouping
// so directories (including navigable links) remain first in both directions.
export function compareEntries(a: Entry, b: Entry, field: SortField, descending: boolean): number {
  if (a.navigable !== b.navigable) {
    return (a.navigable ? -1 : 1) * (descending ? -1 : 1);
  }
  let result: number;
  if (field === "size") {
    result = compare(BigInt(a.size || "-1"), BigInt(b.size || "-1"));
  } else if (field === "modifiedAt") {
    // The backend emits fixed-width UTC timestamps, including nanoseconds.
    result = compare(a.modifiedAt, b.modifiedAt);
  } else {
    result = collator.compare(a[field], b[field]);
  }
  return result || collator.compare(a.name, b.name) || compare(a.name, b.name) || compare(a.id, b.id);
}

export function visibleEntries(entries: Entry[], showHidden: boolean): Entry[] {
  return showHidden ? entries : entries.filter(entry => !entry.hidden);
}

// Keep the same item after a refresh; if it disappeared, keep its nearest index.
export function restoredIndex(ids: string[], current: string | undefined, previousIndex: number): number | null {
  if (!ids.length) return null;
  const found = current === undefined ? -1 : ids.indexOf(current);
  return found >= 0 ? found : Math.max(0, Math.min(previousIndex, ids.length - 1));
}
