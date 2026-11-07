import type { CommandId } from "./commands.ts";

type Key = Pick<KeyboardEvent, "key" | "ctrlKey" | "metaKey" | "altKey" | "shiftKey" | "isComposing">;

// Called only from the file-list container. All unclaimed keys (including
// arrows, Shift+arrows and Space) remain with AG Grid.
export function panelKeyCommand(event: Key, textInput: boolean): CommandId | "refresh" | undefined {
  if (textInput || event.isComposing || event.altKey) return undefined;
  if (event.ctrlKey || event.metaKey) {
    if (event.shiftKey) return undefined;
    if (event.key.toLowerCase() === "a") return "panel.selectAll";
    if (event.key.toLowerCase() === "r") return "refresh";
    return undefined;
  }
  if (event.key === "Tab") return "panel.switch";
  if (event.shiftKey) return undefined;
  if (event.key === "Enter") return "panel.openEntry";
  if (event.key === "Backspace") return "panel.goToParent";
}

export function isTextInput(target: Element): boolean {
  return !!target.closest("input, textarea, select, [contenteditable]:not([contenteditable='false']), [role='textbox']");
}
