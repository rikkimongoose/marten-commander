import { useEffect, useState } from "react";
import { Alert, Box, Button, Checkbox, Dialog, DialogActions, DialogContent, DialogTitle,
  FormControlLabel, Radio, RadioGroup, Stack, Typography } from "@mui/material";
import { formatBytes, type ConflictRequestView } from "./operationState.ts";

interface ConflictDialogProps {
  conflict: ConflictRequestView | null;
  busy: boolean;
  error: string;
  resolve: (action: string, applyToAll: boolean) => void;
  close: () => void;
}

const actionLabels: Record<string, string> = {
  replace: "Заменить существующий файл",
  skip: "Пропустить источник",
  "keep-both": "Сохранить оба с новым именем",
  merge: "Объединить каталоги",
  "cancel-operation": "Отменить всю операцию",
};

function metadata(label: string, value: ConflictRequestView["source"]) {
  const modified = value.modifiedAt ? new Date(value.modifiedAt).toLocaleString() : "неизвестно";
  return <Box>
    <Typography variant="subtitle2">{label}</Typography>
    <Typography sx={{ overflowWrap: "anywhere" }}>{value.path}</Typography>
    <Typography variant="body2" color="text.secondary">
      Тип: {value.kind === "directory" ? "каталог" : "файл"} · Размер: {value.size === null ? "неизвестен" : formatBytes(value.size)} · Изменён: {modified}
    </Typography>
  </Box>;
}

export function ConflictDialog({ conflict, busy, error, resolve, close }: ConflictDialogProps) {
  const actions = conflict?.allowedActions ?? [];
  const [action, setAction] = useState("");
  const [applyToAll, setApplyToAll] = useState(false);

  useEffect(() => {
    if (!conflict) return;
    setAction(actions.includes("skip") ? "skip" : actions[0] ?? "");
    setApplyToAll(false);
  }, [conflict?.conflictId]);

  const mismatch = conflict?.type === "file/directory" || conflict?.type === "directory/file";
  return <Dialog open={!!conflict} onClose={(_, reason) => { if (!busy && (reason === "escapeKeyDown" || reason === "backdropClick")) close(); }}
    aria-labelledby="conflict-dialog-title">
    <DialogTitle id="conflict-dialog-title">Файл уже существует</DialogTitle>
    {conflict && <DialogContent sx={{ minWidth: { xs: 300, sm: 560 } }}>
      <Stack spacing={1.5}>
        {mismatch && <Alert severity="warning">Типы источника и назначения различаются. Автоматическая замена отключена.</Alert>}
        {conflict.type === "directory/directory" && <Alert severity="info">
          Объединение не заменяет вложенные файлы автоматически: каждый вложенный конфликт потребует отдельного решения.
        </Alert>}
        {metadata("Источник", conflict.source)}
        {metadata("Назначение", conflict.destination)}
        {error && <Alert severity="error">{error}</Alert>}
        <RadioGroup value={action} onChange={event => { setAction(event.target.value); setApplyToAll(false); }}>
          {actions.map(value => <FormControlLabel key={value} value={value} control={<Radio disabled={busy} />}
            label={actionLabels[value] ?? value} />)}
        </RadioGroup>
        <FormControlLabel control={<Checkbox checked={applyToAll} disabled={busy || action === "cancel-operation"}
          onChange={event => setApplyToAll(event.target.checked)} />} label="Применить ко всем совместимым конфликтам этой операции" />
        {(action === "replace" || action === "keep-both" || action === "merge") && <Alert severity="warning">
          Решение будет повторно проверено файловой операцией непосредственно перед безопасной записью. Оно само по себе не разрешает безусловную перезапись.
        </Alert>}
      </Stack>
    </DialogContent>}
    <DialogActions>
      <Button onClick={close} disabled={busy}>Закрыть и решить позже</Button>
      <Button variant="contained" onClick={() => resolve(action, applyToAll)} disabled={busy || !action}>
        {busy ? "Применение…" : "Применить"}
      </Button>
    </DialogActions>
  </Dialog>;
}
