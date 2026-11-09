import { useCallback, useEffect, useRef, useState } from "react";
import { Alert, Box, Button, CircularProgress, Divider, IconButton, LinearProgress, Paper, Stack, Tooltip, Typography } from "@mui/material";
import { Events } from "@wailsio/runtime";
import { CancelOperation, ListOperations, ResolveConflict } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/operationservice";
import type { ConflictResolution, Operation } from "../bindings/github.com/rikkimongoose/marten-commander/internal/operations/models";
import { OperationsUpdated } from "../bindings/events";
import { connectOperationUpdates, type OperationAPI } from "./operationClient.ts";
import { applyOperationEvent, formatBytes, mergeOperations, operationIsCancellable, operationProgress,
  pendingConflicts, type ConflictResolutionView, type OperationUpdateEvent, type OperationView } from "./operationState.ts";
import { ConflictDialog } from "./ConflictDialog.tsx";

const labels: Record<string, string> = {
  copy: "Копирование", move: "Перемещение", delete: "Удаление",
  "file.copy": "Копирование", "file.move": "Перемещение", "file.delete": "Удаление",
  queued: "В очереди", running: "Выполняется", "waiting-for-user": "Ожидает решения",
  completed: "Завершена", failed: "Ошибка", cancelled: "Отменена",
};

function errorMessage(error: unknown): string {
  const cause = error instanceof Error && "cause" in error ? error.cause : undefined;
  if (cause && typeof cause === "object" && "message" in cause && typeof cause.message === "string") return cause.message;
  return error instanceof Error ? error.message : String(error);
}

const api: OperationAPI = {
  list: () => ListOperations() as PromiseLike<Operation[] | null>,
  cancel: id => CancelOperation(id) as PromiseLike<Operation>,
  resolveConflict: resolution => ResolveConflict(resolution as ConflictResolution) as PromiseLike<Operation>,
};

export function OperationsCenter({ onOperationFinished }: { onOperationFinished?: (operation: Operation) => void }) {
  const [open, setOpen] = useState(false);
  const [operations, setOperations] = useState<readonly OperationView[]>([]);
  const [loading, setLoading] = useState(true);
  const [failure, setFailure] = useState("");
  const [cancelling, setCancelling] = useState<ReadonlySet<string>>(new Set());
  const [conflictID, setConflictID] = useState<string>();
  const [dismissedConflicts, setDismissedConflicts] = useState<ReadonlySet<string>>(new Set());
  const [conflictBusy, setConflictBusy] = useState(false);
  const [conflictError, setConflictError] = useState("");
  const notifiedFinished = useRef(new Map<string, number>());

  const refresh = useCallback(() => {
    setLoading(true);
    setFailure("");
    void Promise.resolve(api.list()).then(list => setOperations(current => mergeOperations(current, list ?? [])),
      error => setFailure(errorMessage(error))).finally(() => setLoading(false));
  }, []);

  useEffect(() => connectOperationUpdates(api, receive => Events.On(OperationsUpdated, event => receive(event.data as OperationUpdateEvent)),
    list => { setOperations(current => mergeOperations(current, list)); setLoading(false); },
    event => setOperations(current => applyOperationEvent(current, event)),
    error => { setFailure(errorMessage(error)); setLoading(false); }), []);

  useEffect(() => {
    const pending = pendingConflicts(operations);
    if (conflictID && !pending.some(conflict => conflict.conflictId === conflictID)) setConflictID(undefined);
    if (!conflictID) {
      const next = pending.find(conflict => !dismissedConflicts.has(conflict.conflictId));
      if (next) setConflictID(next.conflictId);
    }
  }, [operations, conflictID, dismissedConflicts]);

  useEffect(() => {
    if (!onOperationFinished) return;
    for (const operation of operations) {
      if (operation.state !== "completed" && operation.state !== "failed" && operation.state !== "cancelled") continue;
      if ((notifiedFinished.current.get(operation.operationId) ?? 0) >= operation.updateVersion) continue;
      notifiedFinished.current.set(operation.operationId, operation.updateVersion);
      onOperationFinished(operation as Operation);
    }
  }, [operations, onOperationFinished]);

  function show() { setOpen(true); refresh(); }

  function cancel(id: string) {
    setCancelling(current => new Set(current).add(id));
    setFailure("");
    void Promise.resolve(api.cancel(id)).then(operation => setOperations(current => mergeOperations(current, [operation])),
      error => setFailure(errorMessage(error))).finally(() => setCancelling(current => {
        const next = new Set(current); next.delete(id); return next;
      }));
  }

  function openConflict(id: string) {
    setDismissedConflicts(current => { const next = new Set(current); next.delete(id); return next; });
    setConflictError("");
    setConflictID(id);
  }

  function closeConflict() {
    if (!conflictID || conflictBusy) return;
    setDismissedConflicts(current => new Set(current).add(conflictID));
    setConflictID(undefined);
    setConflictError("");
  }

  function resolveConflict(action: string, applyToAll: boolean) {
    const conflict = pendingConflicts(operations).find(value => value.conflictId === conflictID);
    if (!conflict || conflictBusy) return;
    const resolution: ConflictResolutionView = { operationId: conflict.operationId, conflictId: conflict.conflictId,
      revision: conflict.revision, action, applyToAll, customTargetPath: "" };
    setConflictBusy(true);
    setConflictError("");
    void Promise.resolve(api.resolveConflict(resolution)).then(operation => {
      setOperations(current => mergeOperations(current, [operation]));
      setDismissedConflicts(current => new Set(current).add(conflict.conflictId));
      setConflictID(undefined);
    }, error => {
      setConflictError(errorMessage(error));
      refresh();
    }).finally(() => setConflictBusy(false));
  }

  const active = operations.filter(operation => operationIsCancellable(operation.state)).length;
  return <>
    <Button variant="outlined" size="small" onClick={show} sx={{ flex: "0 0 auto", whiteSpace: "nowrap" }}>
      Операции{active > 0 ? ` (${active})` : ""}
    </Button>
    {open && <Paper role="dialog" aria-label="Файловые операции" data-nonmodal-operation-dialog elevation={8} sx={{
      position: "fixed", zIndex: theme => theme.zIndex.modal, right: 16, bottom: 58,
      width: "min(520px, calc(100vw - 32px))", maxHeight: "min(70vh, 560px)", overflow: "auto", p: 1.5,
    }}>
      <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
        <Typography variant="h6" sx={{ flex: 1 }}>Операции</Typography>
        <Tooltip title="Обновить"><span><IconButton aria-label="Обновить операции" size="small" onClick={refresh} disabled={loading}>↻</IconButton></span></Tooltip>
        <IconButton aria-label="Закрыть операции" size="small" onClick={() => setOpen(false)}>×</IconButton>
      </Box>
      {failure && <Alert severity="error" onClose={() => setFailure("")} sx={{ my: 1 }}>{failure}</Alert>}
      {loading && operations.length === 0 && <Box sx={{ p: 3, textAlign: "center" }}><CircularProgress size={28} /></Box>}
      {!loading && operations.length === 0 && <Typography color="text.secondary" sx={{ py: 2 }}>Операций пока нет.</Typography>}
      <Stack divider={<Divider flexItem />}>
        {operations.map(operation => {
          const progress = operationProgress(operation);
          const busy = cancelling.has(operation.operationId);
          return <Box key={operation.operationId} sx={{ py: 1 }}>
            <Box sx={{ display: "flex", alignItems: "baseline", gap: 1 }}>
              <Typography sx={{ flex: 1, fontWeight: 600 }}>{labels[operation.type] ?? operation.type}</Typography>
              <Typography variant="body2" color="text.secondary">{labels[operation.state] ?? operation.state}</Typography>
            </Box>
            {operation.progress.currentFile && <Typography variant="body2" title={operation.progress.currentFile} noWrap>{operation.progress.currentFile}</Typography>}
            <Typography variant="caption" color="text.secondary" component="div">
              Файлы: {operation.progress.processedFiles}{operation.progress.totalFiles === null ? " / ?" : ` / ${operation.progress.totalFiles}`}
              {" · "}Данные: {formatBytes(operation.progress.processedBytes)}{operation.progress.totalBytes === null ? " / ?" : ` / ${formatBytes(operation.progress.totalBytes)}`}
            </Typography>
            <LinearProgress variant={progress.determinate ? "determinate" : "indeterminate"} value={progress.value} sx={{ my: 0.75 }} />
            {operation.error && <Alert severity="error" sx={{ my: 0.75 }}>{operation.error.message}</Alert>}
            {operation.pendingConflict && <Button size="small" color="warning"
              onClick={() => openConflict(operation.pendingConflict!.conflictId)}>Разрешить конфликт</Button>}
            {operationIsCancellable(operation.state) && <Button size="small" onClick={() => cancel(operation.operationId)} disabled={busy}>
              {busy ? "Отмена…" : "Отмена"}
            </Button>}
          </Box>;
        })}
      </Stack>
    </Paper>}
    <ConflictDialog conflict={pendingConflicts(operations).find(value => value.conflictId === conflictID) ?? null}
      busy={conflictBusy} error={conflictError} resolve={resolveConflict} close={closeConflict} />
  </>;
}
