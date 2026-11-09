import { useEffect, useRef, useState } from "react";
import { Alert, Button, Dialog, DialogActions, DialogContent, DialogTitle, List, ListItem,
  TextField, Typography } from "@mui/material";

interface CopyDialogProps {
  open: boolean;
  sources: readonly string[];
  initialDestination: string;
  busy: boolean;
  error: string;
  submit: (destination: string) => void;
  cancel: () => void;
  closed: () => void;
}

export function CopyDialog({ open, sources, initialDestination, busy, error, submit, cancel, closed }: CopyDialogProps) {
  const [destination, setDestination] = useState("");
  const [validation, setValidation] = useState("");
  const input = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!open) return;
    setDestination(initialDestination);
    setValidation("");
    const frame = requestAnimationFrame(() => input.current?.focus());
    return () => cancelAnimationFrame(frame);
  }, [open, initialDestination]);

  function confirm() {
    if (busy) return;
    const message = !destination.trim() || !destination.startsWith("/")
      ? "Укажите абсолютный локальный каталог назначения." : "";
    setValidation(message);
    if (!message) submit(destination);
  }

  return <Dialog open={open} onClose={(_, reason) => { if (!busy && reason === "escapeKeyDown") cancel(); }}
    slotProps={{ paper: { component: "form", onSubmit: event => { event.preventDefault(); confirm(); } },
      transition: { onExited: () => queueMicrotask(closed) } }}>
    <DialogTitle>Копировать</DialogTitle>
    <DialogContent sx={{ minWidth: { xs: 300, sm: 520 } }}>
      <Typography variant="body2" sx={{ mb: 0.5 }}>Источники: {sources.length}</Typography>
      <List dense sx={{ maxHeight: 160, overflow: "auto", mb: 1 }}>
        {sources.map((name, index) => <ListItem key={`${index}-${name}`} sx={{ py: 0.25, overflowWrap: "anywhere" }}>{name}</ListItem>)}
      </List>
      {(error || validation) && <Alert severity="error" sx={{ mb: 1 }}>{error || validation}</Alert>}
      <TextField inputRef={input} fullWidth label="Каталог назначения" value={destination} disabled={busy}
        error={!!validation} onChange={event => { setDestination(event.target.value); setValidation(""); }} />
    </DialogContent>
    <DialogActions>
      <Button onClick={cancel} disabled={busy}>Отмена</Button>
      <Button type="submit" variant="contained" disabled={busy || !destination.trim()}>Копировать</Button>
    </DialogActions>
  </Dialog>;
}
