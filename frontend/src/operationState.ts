export interface OperationProgressView {
  readonly processedFiles: number;
  readonly totalFiles: number | null;
  readonly processedBytes: number;
  readonly totalBytes: number | null;
  readonly currentFile: string;
}

export interface OperationErrorView { readonly code: string; readonly message: string }

export interface ConflictMetadataView {
  readonly path: string;
  readonly kind: string;
  readonly size: number | null;
  readonly modifiedAt: string | null;
}

export interface ConflictRequestView {
  readonly operationId: string;
  readonly conflictId: string;
  readonly type: string;
  readonly source: ConflictMetadataView;
  readonly destination: ConflictMetadataView;
  readonly allowedActions: readonly string[] | null;
  readonly revision: number;
}

export interface ConflictResolutionView {
  readonly operationId: string;
  readonly conflictId: string;
  readonly revision: number;
  readonly action: string;
  readonly applyToAll: boolean;
  readonly customTargetPath: string;
}

export interface OperationView {
  readonly cancellationRequested?: boolean;
  readonly operationId: string;
  readonly type: string;
  readonly state: string;
  readonly progress: OperationProgressView;
  readonly error: OperationErrorView | null;
  readonly pendingConflict: ConflictRequestView | null;
  readonly updateVersion: number;
  readonly createdAt: string;
  readonly finishedAt: string | null;
}

export interface OperationUpdateEvent {
  readonly operationId: string;
  readonly state: string;
  readonly progress: OperationProgressView;
  readonly updateVersion: number;
  readonly operation: OperationView;
}

export function mergeOperations(current: readonly OperationView[], incoming: readonly OperationView[]): readonly OperationView[] {
  const byId = new Map(current.map(operation => [operation.operationId, operation]));
  for (const operation of incoming) {
    const saved = byId.get(operation.operationId);
    if (!saved || operation.updateVersion >= saved.updateVersion) byId.set(operation.operationId, operation);
  }
  return [...byId.values()].sort((left, right) => right.createdAt.localeCompare(left.createdAt));
}

export function applyOperationEvent(current: readonly OperationView[], event: OperationUpdateEvent): readonly OperationView[] {
  if (!event || event.operationId !== event.operation?.operationId || event.updateVersion !== event.operation?.updateVersion) return current;
  return mergeOperations(current, [event.operation]);
}

export function operationIsCancellable(state: string): boolean {
  return state === "queued" || state === "running" || state === "waiting-for-user";
}

export function pendingConflicts(operations: readonly OperationView[]): readonly ConflictRequestView[] {
  return operations.flatMap(operation => operation.pendingConflict ? [operation.pendingConflict] : []);
}

export function operationProgress(operation: OperationView): { determinate: boolean; value: number } {
  const progress = operation.progress;
  if (progress.totalBytes !== null && progress.totalBytes > 0) {
    return { determinate: true, value: Math.min(100, progress.processedBytes / progress.totalBytes * 100) };
  }
  if (progress.totalFiles !== null && progress.totalFiles > 0) {
    return { determinate: true, value: Math.min(100, progress.processedFiles / progress.totalFiles * 100) };
  }
  if (operation.state === "completed") return { determinate: true, value: 100 };
  return { determinate: false, value: 0 };
}

export function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value < 0) return "—";
  const units = ["Б", "КБ", "МБ", "ГБ", "ТБ"];
  let amount = value, unit = 0;
  while (amount >= 1024 && unit < units.length - 1) { amount /= 1024; unit++; }
  return `${unit === 0 ? amount.toFixed(0) : amount.toFixed(amount >= 10 ? 1 : 2)} ${units[unit]}`;
}
