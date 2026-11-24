import { useEffect, useRef, useState } from "react";
import { Alert, Box, Button, Dialog, DialogActions, DialogContent, DialogTitle, Divider, FormControl,
  InputLabel, List, ListItemButton, ListItemText, Menu, MenuItem, Select, Stack, TextField, Typography } from "@mui/material";
import { CancelConnection, Connect, DeleteProfile, Disconnect, GetConnectionStatus, ListProfiles,
  ResolveHostKey, SaveProfile } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/sftpservice";
import type { AttemptStatus, ConnectionProfile, HostKeyChallenge } from "../bindings/github.com/rikkimongoose/marten-commander/internal/sftp/models";
import { emptySFTPProfile, profileForSave, sftpProfileError } from "./sftpProfile";

const activeAttempt = (status?: AttemptStatus) => status?.state === "connecting" || status?.state === "awaiting_host_key";
const lockedConnection = (status?: AttemptStatus) => activeAttempt(status) || status?.state === "connected";

export function SFTPConnections({ connected, disconnected }: {
  connected: (profile: ConnectionProfile, connectionId: string) => void;
  disconnected: (connectionId: string) => void;
}) {
  const [anchor, setAnchor] = useState<HTMLElement>();
  const [open, setOpen] = useState(false);
  const [profiles, setProfiles] = useState<ConnectionProfile[]>([]);
  const [selectedID, setSelectedID] = useState("");
  const [form, setForm] = useState<ConnectionProfile>(emptySFTPProfile);
  const [editing, setEditing] = useState(false);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [deleteConfirm, setDeleteConfirm] = useState(false);
  const [secret, setSecret] = useState("");
  const [status, setStatus] = useState<AttemptStatus>();
  const [error, setError] = useState("");
  const polling = useRef(0);
  const openRef = useRef(false);
  const connectPending = useRef(false);
  const presentedConnection = useRef("");
  const selected = profiles.find(profile => profile.id === selectedID);

  async function loadProfiles() {
    setLoading(true); setError("");
    try {
      const values = await ListProfiles();
      setProfiles(values ?? []);
      setSelectedID(current => values?.some(profile => profile.id === current) ? current : values?.[0]?.id ?? "");
    } catch (reason) { setError(message(reason)); }
    finally { setLoading(false); }
  }

  useEffect(() => {
    if (open) void loadProfiles();
  }, [open]);

  useEffect(() => {
    if (!status || !activeAttempt(status)) return;
    let live = true;
    const poll = async () => {
      try {
        const next = await GetConnectionStatus(status.attemptId);
        if (!live) return;
        setStatus(next);
        if (activeAttempt(next)) polling.current = window.setTimeout(poll, 200);
      } catch (reason) {
        if (live) setError(message(reason));
      }
    };
    polling.current = window.setTimeout(poll, 100);
    return () => { live = false; window.clearTimeout(polling.current); };
  }, [status?.attemptId, status?.state]);

  useEffect(() => {
    if (status?.state !== "connected" || !status.connectionId || presentedConnection.current === status.connectionId) return;
    const profile = profiles.find(value => value.id === status.profileId);
    if (!profile) return;
    presentedConnection.current = status.connectionId;
    connected(profile, status.connectionId);
    openRef.current = false;
    setOpen(false);
  }, [status?.state, status?.connectionId, status?.profileId, profiles, connected]);

  function choose(profile: ConnectionProfile) {
    if (lockedConnection(status)) return;
    setSelectedID(profile.id); setEditing(false); setSecret(""); setError("");
  }

  function edit(profile?: ConnectionProfile) {
    if (lockedConnection(status)) return;
    setForm(profile ? { ...profile } : emptySFTPProfile());
    setEditing(true); setError("");
  }

  async function save() {
    if (saving) return;
    const value = profileForSave(form);
    const validation = sftpProfileError(value);
    if (validation) { setError(validation); return; }
    setSaving(true); setError("");
    try {
      const saved = await SaveProfile(value);
      await loadProfiles();
      setSelectedID(saved.id); setEditing(false);
    } catch (reason) { setError(message(reason)); }
    finally { setSaving(false); }
  }

  async function remove() {
    if (!selected || deleting) return;
    setDeleting(true); setError("");
    try {
      await DeleteProfile(selected.id);
      setDeleteConfirm(false); setStatus(undefined); setSecret("");
      await loadProfiles();
    } catch (reason) { setError(message(reason)); }
    finally { setDeleting(false); }
  }

  async function connect() {
    if (!selected || activeAttempt(status) || connectPending.current) return;
    if (selected.authMethod === "password" && !secret) { setError("Введите пароль для подключения."); return; }
    connectPending.current = true;
    setError("");
    try {
      const next = await Connect({ profileId: selected.id, secret });
      setSecret("");
      if (!openRef.current) {
        await CancelConnection(next.attemptId);
        return;
      }
      setStatus(next);
    } catch (reason) { setSecret(""); setError(message(reason)); }
    finally { connectPending.current = false; }
  }

  async function cancel() {
    if (!status || !activeAttempt(status)) return;
    try { await CancelConnection(status.attemptId); setStatus(await GetConnectionStatus(status.attemptId)); }
    catch (reason) { setError(message(reason)); }
  }

  async function disconnect() {
    if (!status?.connectionId) return;
    const connectionId = status.connectionId;
    try { await Disconnect(connectionId); disconnected(connectionId); presentedConnection.current = ""; setStatus(undefined); }
    catch (reason) { setError(message(reason)); }
  }

  async function resolveHostKey(challenge: HostKeyChallenge, accept: boolean) {
    if (!status) return;
    try {
      await ResolveHostKey(status.attemptId, challenge.id, accept);
      setStatus(await GetConnectionStatus(status.attemptId));
    } catch (reason) { setError(message(reason)); }
  }

  function close() {
    openRef.current = false;
    setAnchor(undefined);
    if (activeAttempt(status)) void cancel();
    setDeleteConfirm(false); setEditing(false); setSecret(""); setOpen(false);
  }

  const connectionLabel = status?.state === "connecting" ? "Подключение…"
    : status?.state === "awaiting_host_key" ? "Ожидание подтверждения ключа сервера"
    : status?.state === "connected" ? "Подключено"
    : status?.state === "cancelled" ? "Подключение отменено"
    : status?.state === "error" ? "Ошибка подключения" : "Отключено";

  return <>
    <Button onClick={event => setAnchor(event.currentTarget)}>Сеть</Button>
    <Menu anchorEl={anchor} open={!!anchor} onClose={() => setAnchor(undefined)}>
      <MenuItem onClick={() => { setAnchor(undefined); openRef.current = true; setOpen(true); }}>SFTP-подключения…</MenuItem>
    </Menu>
    <Dialog open={open} fullWidth maxWidth="md" onClose={(_, reason) => {
      if (reason === "escapeKeyDown" || reason === "backdropClick") close();
    }}>
      <DialogTitle>SFTP-подключения</DialogTitle>
      <DialogContent dividers sx={{ minHeight: 480 }}>
        {error && <Alert severity="error" sx={{ mb: 1 }} onClose={() => setError("")}>{error}</Alert>}
        <Box sx={{ display: "grid", gridTemplateColumns: { xs: "1fr", md: "260px 1fr" }, gap: 2 }}>
          <Box>
            <Stack direction="row" spacing={1} sx={{ mb: 1 }}>
              <Button size="small" disabled={lockedConnection(status)} onClick={() => edit()}>Создать</Button>
              <Button size="small" disabled={!selected || lockedConnection(status)} onClick={() => edit(selected)}>Изменить</Button>
              <Button size="small" color="error" disabled={!selected || lockedConnection(status)} onClick={() => setDeleteConfirm(true)}>Удалить</Button>
            </Stack>
            <List dense aria-label="SFTP-профили" sx={{ border: 1, borderColor: "divider", borderRadius: 1 }}>
              {loading && <ListItemText sx={{ px: 2 }} primary="Загрузка…" />}
              {!loading && !profiles.length && <ListItemText sx={{ px: 2 }} primary="Нет сохранённых профилей" />}
              {profiles.map(profile => <ListItemButton key={profile.id} selected={profile.id === selectedID}
                onClick={() => choose(profile)}>
                <ListItemText primary={profile.name} secondary={`${profile.username}@${profile.host}:${profile.port}`} />
              </ListItemButton>)}
            </List>
          </Box>
          <Box>
            {editing ? <>
              <ProfileForm value={form} disabled={saving} change={setForm} />
              <Stack direction="row" spacing={1} sx={{ mt: 2, justifyContent: "flex-end" }}>
                <Button disabled={saving} onClick={() => setEditing(false)}>Отмена редактирования</Button>
                <Button disabled={saving} variant="contained" onClick={() => void save()}>Сохранить профиль</Button>
              </Stack>
            </> : selected ? <>
              <Typography variant="h6">{selected.name}</Typography>
              <Typography>{selected.username}@{selected.host}:{selected.port}</Typography>
              <Typography color="text.secondary">Аутентификация: {authLabel(selected.authMethod)}</Typography>
              {selected.remotePath && <Typography color="text.secondary">Начальный каталог: {selected.remotePath}</Typography>}
              <Divider sx={{ my: 2 }} />
              <Typography aria-live="polite" sx={{ mb: 1 }}>Состояние: {connectionLabel}</Typography>
              {(selected.authMethod === "password" || selected.authMethod === "key") && status?.state !== "connected" &&
                <TextField fullWidth type="password" autoComplete="off" value={secret}
                  label={selected.authMethod === "password" ? "Пароль" : "Passphrase ключа (если требуется)"}
                  onChange={event => setSecret(event.target.value)} sx={{ mb: 2 }} />}
              {status?.state === "error" && <Alert severity="error" sx={{ mb: 1 }}>{status.error}</Alert>}
              {status?.state === "connected"
                ? <Button variant="contained" color="warning" onClick={() => void disconnect()}>Отключиться</Button>
                : activeAttempt(status)
                  ? <Button variant="contained" color="warning" onClick={() => void cancel()}>Отмена</Button>
                  : <Button variant="contained" onClick={() => void connect()}>Подключиться</Button>}
            </> : <Typography color="text.secondary">Выберите или создайте профиль.</Typography>}
          </Box>
        </Box>
      </DialogContent>
      <DialogActions>
        <Button onClick={close}>Закрыть</Button>
      </DialogActions>
    </Dialog>
    <Dialog open={deleteConfirm} onClose={() => !deleting && setDeleteConfirm(false)}>
      <DialogTitle>Удалить SFTP-профиль?</DialogTitle>
      <DialogContent><Typography>Профиль «{selected?.name}» будет удалён. Другие профили не изменятся.</Typography></DialogContent>
      <DialogActions><Button disabled={deleting} onClick={() => setDeleteConfirm(false)}>Отмена</Button>
        <Button disabled={deleting} color="error" variant="contained" onClick={() => void remove()}>Удалить</Button></DialogActions>
    </Dialog>
    <HostKeyDialog challenge={status?.state === "awaiting_host_key" ? status.challenge : undefined}
      resolve={resolveHostKey} />
  </>;
}

function ProfileForm({ value, disabled, change }: { value: ConnectionProfile; disabled: boolean;
  change: (profile: ConnectionProfile) => void }) {
  const field = <K extends keyof ConnectionProfile>(key: K, next: ConnectionProfile[K]) => change({ ...value, [key]: next });
  return <Stack spacing={2} component="form" onSubmit={event => event.preventDefault()}>
    <TextField autoFocus required label="Название" value={value.name} disabled={disabled} onChange={event => field("name", event.target.value)} />
    <TextField required label="Hostname или IP" value={value.host} disabled={disabled} onChange={event => field("host", event.target.value)} />
    <TextField required type="number" label="Порт" value={value.port} disabled={disabled}
      slotProps={{ htmlInput: { min: 1, max: 65535 } }} onChange={event => field("port", Number(event.target.value))} />
    <TextField required label="Пользователь" value={value.username} disabled={disabled} onChange={event => field("username", event.target.value)} />
    <FormControl><InputLabel id="sftp-auth-label">Аутентификация</InputLabel>
      <Select labelId="sftp-auth-label" label="Аутентификация" value={value.authMethod} disabled={disabled}
        onChange={event => field("authMethod", event.target.value as ConnectionProfile["authMethod"])}>
        <MenuItem value="agent">SSH agent</MenuItem><MenuItem value="key">Приватный ключ</MenuItem><MenuItem value="password">Пароль</MenuItem>
      </Select>
    </FormControl>
    {value.authMethod === "key" && <TextField required label="Путь к приватному ключу" value={value.privateKeyPath ?? ""}
      disabled={disabled} onChange={event => field("privateKeyPath", event.target.value)} />}
    <TextField label="Начальный каталог (POSIX)" placeholder="/home/user" value={value.remotePath ?? ""}
      disabled={disabled} onChange={event => field("remotePath", event.target.value)} />
    <TextField type="number" label="Таймаут, секунд" value={value.timeoutSeconds ?? 15} disabled={disabled}
      slotProps={{ htmlInput: { min: 1, max: 300 } }} onChange={event => field("timeoutSeconds", Number(event.target.value))} />
  </Stack>;
}

function HostKeyDialog({ challenge, resolve }: { challenge?: HostKeyChallenge | null;
  resolve: (challenge: HostKeyChallenge, accept: boolean) => Promise<void> }) {
  const [busy, setBusy] = useState(false);
  if (!challenge) return null;
  const decide = async (accept: boolean) => { if (busy) return; setBusy(true); try { await resolve(challenge, accept); } finally { setBusy(false); } };
  return <Dialog open onClose={() => void decide(false)} aria-labelledby="host-key-title">
    <DialogTitle id="host-key-title">Новый ключ SSH-сервера</DialogTitle>
    <DialogContent>
      <Alert severity="warning" sx={{ mb: 2 }}>Проверьте fingerprint по доверенному каналу до принятия ключа.</Alert>
      <Typography>Сервер: {challenge.host}:{challenge.port}</Typography>
      <Typography>Алгоритм: {challenge.algorithm}</Typography>
      <Typography sx={{ fontFamily: "monospace", overflowWrap: "anywhere" }}>SHA256: {challenge.fingerprint.replace(/^SHA256:/, "")}</Typography>
    </DialogContent>
    <DialogActions><Button disabled={busy} onClick={() => void decide(false)}>Отказаться</Button>
      <Button disabled={busy} variant="contained" onClick={() => void decide(true)}>Доверять и подключиться</Button></DialogActions>
  </Dialog>;
}

function authLabel(value: ConnectionProfile["authMethod"]): string {
  return value === "agent" ? "SSH agent" : value === "key" ? "приватный ключ" : "пароль";
}

function message(reason: unknown): string {
  if (reason instanceof Error) {
    const cause = (reason as Error & { cause?: { message?: unknown } }).cause;
    if (typeof cause?.message === "string") return cause.message;
    return reason.message;
  }
  return String(reason);
}
