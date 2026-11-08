import { useEffect, useRef, useState } from "react";
import { Alert, Button, Dialog, DialogActions, DialogContent, DialogTitle, TextField, Typography } from "@mui/material";
import { directoryNameError } from "./fileCommands.ts";

interface MkdirDialogProps {
  open: boolean;
  destination: string;
  busy: boolean;
  error: string;
  submit: (name: string) => void;
  cancel: () => void;
  closed: () => void;
}

export function MkdirDialog({ open, destination, busy, error, submit, cancel, closed }: MkdirDialogProps) {
  const [name, setName] = useState("");
  const [validation, setValidation] = useState("");
  const input = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!open) return;
    setName("");
    setValidation("");
    const frame = requestAnimationFrame(() => input.current?.focus());
    return () => cancelAnimationFrame(frame);
  }, [open, destination]);

  function confirm() {
    if (busy) return;
    const message = directoryNameError(name);
    setValidation(message);
    if (!message) submit(name);
  }

  return <Dialog open={open}
    onClose={(_, reason) => { if (!busy && reason === "escapeKeyDown") cancel(); }}
    slotProps={{
      paper: { component: "form", onSubmit: event => { event.preventDefault(); confirm(); } },
      transition: { onExited: () => queueMicrotask(closed) },
    }}>
    <DialogTitle>Создать каталог</DialogTitle>
    <DialogContent sx={{ minWidth: { xs: 280, sm: 440 } }}>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 1, overflowWrap: "anywhere" }}>
        Каталог назначения: {destination}
      </Typography>
      {(error || validation) && <Alert severity="error" sx={{ mb: 1 }}>{error || validation}</Alert>}
      <TextField inputRef={input} fullWidth label="Имя нового каталога" value={name} disabled={busy}
        error={!!validation} onChange={event => { setName(event.target.value); setValidation(""); }} />
    </DialogContent>
    <DialogActions>
      <Button onClick={cancel} disabled={busy}>Отмена</Button>
      <Button type="submit" variant="contained" disabled={busy || !name.trim()}>Создать</Button>
    </DialogActions>
  </Dialog>;
}
