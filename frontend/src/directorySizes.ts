import type { Directory } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/models.ts";
import type { PanelRow } from "./panelRows.ts";

export type DirectorySizeStatus =
  | { readonly state: "counting" }
  | { readonly state: "complete"; readonly size: string }
  | { readonly state: "failed" };

export type DirectorySizeResult = { readonly location: string; readonly size: string };
type Reader = (location: string) => PromiseLike<DirectorySizeResult>;
type Publish = (location: string, status: DirectorySizeStatus) => void;

export function directorySizeTarget(directory: Directory | undefined, row: PanelRow | undefined): string | null {
  return directory?.source === "local" && row?.kind === "directory" && !row.parent ? row.id : null;
}

export function applyDirectorySizes(rows: PanelRow[], statuses: Readonly<Record<string, DirectorySizeStatus>>): PanelRow[] {
  return rows.map(row => {
    const status = !row.parent && row.kind === "directory" ? statuses[row.id] : undefined;
    if (!status) return row;
    return { ...row, size: status.state === "complete" ? status.size : "", directorySize: status };
  });
}

// Each request is identified by the directory location captured on Space.
// Generation checks also prevent an older retry from replacing a newer result.
export class DirectorySizeRequests {
  private readonly generations = new Map<string, number>();
  private readonly read: Reader;
  private readonly publish: Publish;
  private disposed = false;

  constructor(read: Reader, publish: Publish) {
    this.read = read;
    this.publish = publish;
  }

  start(location: string): void {
    if (this.disposed) return;
    const generation = (this.generations.get(location) ?? 0) + 1;
    this.generations.set(location, generation);
    this.publish(location, { state: "counting" });
    void Promise.resolve(this.read(location)).then(result => {
      if (!this.current(location, generation)) return;
      if (result.location !== location || !/^\d+$/.test(result.size)) {
        this.publish(location, { state: "failed" });
        return;
      }
      this.publish(location, { state: "complete", size: result.size });
    }, () => {
      if (this.current(location, generation)) this.publish(location, { state: "failed" });
    });
  }

  dispose(): void {
    this.disposed = true;
    this.generations.clear();
  }

  private current(location: string, generation: number): boolean {
    return !this.disposed && this.generations.get(location) === generation;
  }
}
