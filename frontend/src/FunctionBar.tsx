import { useEffect } from "react";
import { Box, Button } from "@mui/material";
import type { CommandAvailability, CommandId } from "./commands.ts";
import { functionCommandForKey, functionCommands, invokeFunctionCommand } from "./functionCommands.ts";
import { isTextInput } from "./panelKeyboard.ts";

interface FunctionBarProps {
  availability: (id: CommandId) => CommandAvailability;
  dispatch: (id: CommandId) => void;
}

function keyboardIsBlocked(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false;
  return isTextInput(target) || !!target.closest(".ag-cell-editing, [role='dialog'], [aria-modal='true']") ||
    !!document.querySelector(".ag-cell-editing, [role='dialog']:not([data-nonmodal-operation-dialog]), [aria-modal='true']");
}

export function FunctionBar({ availability, dispatch }: FunctionBarProps) {
  const invoke = (id: CommandId) => invokeFunctionCommand(id, availability, dispatch);

  useEffect(() => {
    const keydown = (event: KeyboardEvent) => {
      const id = functionCommandForKey(event, keyboardIsBlocked(event.target));
      if (!id) return;
      // Own F3-F8 while the main UI is active, even when a command is disabled,
      // so browser defaults such as F5 reload cannot replace the app state.
      event.preventDefault();
      invokeFunctionCommand(id, availability, dispatch);
    };
    window.addEventListener("keydown", keydown);
    return () => window.removeEventListener("keydown", keydown);
  }, [availability, dispatch]);

  return (
    <Box component="nav" aria-label="Функциональные команды" sx={{
      display: "grid", gridTemplateColumns: "repeat(6, minmax(96px, 1fr))",
      gap: 0.5, overflowX: "auto", flex: "0 0 auto",
    }}>
      {functionCommands.map(command => {
        const state = availability(command.id);
        return <Button key={command.id} variant="outlined" size="small" disabled={!state.enabled}
          aria-keyshortcuts={command.shortcut} title={state.enabled ? undefined : state.reason}
          onClick={() => invoke(command.id)} sx={{ minWidth: 96, whiteSpace: "nowrap" }}>
          <Box component="span" sx={{ fontWeight: 700, mr: 0.75 }}>{command.shortcut}</Box>{command.label}
        </Button>;
      })}
    </Box>
  );
}
