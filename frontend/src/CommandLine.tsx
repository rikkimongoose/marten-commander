import { useState, type FormEvent, type KeyboardEvent } from "react";
import { Box, TextField, Typography } from "@mui/material";
import type { CommandId, JsonObject } from "./commands.ts";
import { parseCommandLine } from "./commandLine.ts";

interface CommandLineProps {
  path: string;
  dispatch: (id: CommandId, params?: JsonObject) => void;
  report: (message: string) => void;
  returnToPanel: () => void;
}

export function CommandLine({ path, dispatch, report, returnToPanel }: CommandLineProps) {
  const [value, setValue] = useState("");
  const [history, setHistory] = useState<readonly string[]>([]);
  const [historyIndex, setHistoryIndex] = useState<number>();
  const [scratch, setScratch] = useState("");

  function submit(event: FormEvent) {
    event.preventDefault();
    const original = value.trim();
    const parsed = parseCommandLine(value);
    if (original !== "") setHistory(previous => previous[previous.length - 1] === original ? previous : [...previous, original]);
    setHistoryIndex(undefined);
    setScratch("");
    if (parsed.kind === "empty") return;
    if (parsed.kind === "home") dispatch("panel.goHome");
    else if (parsed.kind === "navigate") dispatch("panel.goToPath", { path: parsed.path });
    else if (parsed.kind === "external") report("Выполнение внешних команд пока не поддерживается.");
    else report(parsed.message);
    setValue("");
  }

  function historyKey(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === "Tab" && !event.ctrlKey && !event.metaKey && !event.altKey && !event.shiftKey) {
      event.preventDefault();
      returnToPanel();
      return;
    }
    if (event.ctrlKey || event.metaKey || event.altKey || event.shiftKey || (event.key !== "ArrowUp" && event.key !== "ArrowDown")) return;
    event.preventDefault();
    if (history.length === 0) return;
    if (event.key === "ArrowUp") {
      const next = historyIndex === undefined ? history.length - 1 : Math.max(0, historyIndex - 1);
      if (historyIndex === undefined) setScratch(value);
      setHistoryIndex(next);
      setValue(history[next]);
    } else if (historyIndex !== undefined) {
      const next = historyIndex + 1;
      if (next >= history.length) {
        setHistoryIndex(undefined);
        setValue(scratch);
      } else {
        setHistoryIndex(next);
        setValue(history[next]);
      }
    }
  }

  return (
    <Box component="form" aria-label="Командная строка" onSubmit={submit}
      sx={{ display: "flex", alignItems: "center", gap: 1, minWidth: 0, flex: "0 0 auto" }}>
      <Typography title={path} sx={{ maxWidth: "45%", flexShrink: 0, overflow: "hidden", textOverflow: "ellipsis",
        whiteSpace: "nowrap", fontFamily: "monospace", fontSize: 13 }}>{path || "…"} &gt;</Typography>
      <TextField value={value} onChange={event => { setValue(event.target.value); setHistoryIndex(undefined); }}
        onKeyDown={historyKey} size="small" fullWidth autoComplete="off"
        slotProps={{ htmlInput: { "aria-label": "Команда", spellCheck: false } }} />
    </Box>
  );
}
