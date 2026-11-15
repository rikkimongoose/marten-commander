import type { CommandAvailability, CommandId } from "./commands.ts";

export interface FunctionCommand {
  readonly shortcut: `F${3 | 4 | 5 | 6 | 7 | 8}`;
  readonly label: string;
  readonly id: CommandId;
}

export const functionCommands: readonly FunctionCommand[] = Object.freeze([
  { shortcut: "F3", label: "Просмотр", id: "file.view" },
  { shortcut: "F4", label: "Правка", id: "file.edit" },
  { shortcut: "F5", label: "Копировать", id: "file.copy" },
  { shortcut: "F6", label: "Переместить", id: "file.move" },
  { shortcut: "F7", label: "Каталог", id: "file.mkdir" },
  { shortcut: "F8", label: "Удалить", id: "file.trash" },
]);

type FunctionKeyEvent = Pick<KeyboardEvent,
  "key" | "ctrlKey" | "metaKey" | "altKey" | "shiftKey" | "isComposing">;

export function functionCommandForKey(event: FunctionKeyEvent, blocked: boolean): CommandId | undefined {
  if (blocked || event.isComposing || event.ctrlKey || event.metaKey || event.altKey || event.shiftKey) return undefined;
  return functionCommands.find(command => command.shortcut === event.key)?.id;
}

export function invokeFunctionCommand(
  id: CommandId,
  availability: (id: CommandId) => CommandAvailability,
  dispatch: (id: CommandId) => void,
): boolean {
  if (!availability(id).enabled) return false;
  dispatch(id);
  return true;
}
