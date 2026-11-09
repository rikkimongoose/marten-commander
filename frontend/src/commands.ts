export type PanelId = "left" | "right";
export type CommandId = "panel.selectAll" | "panel.goToLocation" | "panel.openEntry" | "panel.goToParent" | "panel.switch" |
  "panel.goToPath" | "panel.goHome" | "file.open" | "file.view" | "file.edit" | "file.copy" | "file.move" |
  "file.mkdir" | "file.delete";

// Locations are base64url-encoded absolute native paths, as in Directory/Entry
// DTOs. Display strings cannot address files with escaped or non-UTF-8 names.
export interface PanelContext {
  readonly directoryLocation: string | null;
  readonly selectedLocations: readonly string[];
  readonly focusedLocation: string | null;
}

export interface CommandContext {
  readonly activePanel: PanelId;
  readonly left: PanelContext;
  readonly right: PanelContext;
}

export type JsonValue = null | boolean | number | string | readonly JsonValue[] | JsonObject;
export type JsonObject = { readonly [key: string]: JsonValue };
export interface CommandRequest {
  readonly id: string;
  readonly type: string;
  readonly context: CommandContext;
  readonly params: JsonObject;
}

export type CommandErrorCode = "unknown_command" | "duplicate_command" | "unavailable" |
  "invalid_context" | "invalid_request" | "invalid_params" | "open_failed" | "mkdir_failed" | "copy_failed" | "handler_failed";

export class CommandError extends Error {
  readonly code: CommandErrorCode;
  readonly cause: unknown;

  constructor(code: CommandErrorCode, message: string, cause?: unknown) {
    super(message);
    this.name = "CommandError";
    this.code = code;
    this.cause = cause;
  }
}

export function commandErrorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export function sourceAndTarget(context: CommandContext) {
  return { source: context[context.activePanel], target: context[context.activePanel === "left" ? "right" : "left"] };
}

const noLocations: readonly string[] = Object.freeze([]);

// File commands operate on the explicit Ctrl/Shift selection. When it is
// empty, the row under the independent keyboard cursor is the sole operand.
export function effectiveSelection(panel: PanelContext): readonly string[] {
  if (panel.selectedLocations.length > 0) return panel.selectedLocations;
  return panel.focusedLocation === null ? noLocations : Object.freeze([panel.focusedLocation]);
}

export function snapshotContext(context: CommandContext): CommandContext {
  const panel = (value: PanelContext): PanelContext => {
    if (!value || (value.directoryLocation !== null && typeof value.directoryLocation !== "string") ||
      (value.focusedLocation !== null && typeof value.focusedLocation !== "string") ||
      !Array.isArray(value.selectedLocations) || value.selectedLocations.some(id => typeof id !== "string")) {
      throw new CommandError("invalid_context", "Некорректный контекст панели.");
    }
    return Object.freeze({
      directoryLocation: value.directoryLocation,
      selectedLocations: Object.freeze([...value.selectedLocations]),
      focusedLocation: value.focusedLocation,
    });
  };
  if (!context || (context.activePanel !== "left" && context.activePanel !== "right")) {
    throw new CommandError("invalid_context", "Неизвестная активная панель.");
  }
  return Object.freeze({ activePanel: context.activePanel, left: panel(context.left), right: panel(context.right) });
}

// Copy only JSON data; reject class instances, cycles, undefined and non-finite
// numbers rather than silently changing parameters at the Wails boundary.
function snapshotParams(params: JsonObject): JsonObject {
  const parents = new Set<object>();
  function copy(value: JsonValue): JsonValue {
    if (value === null || typeof value === "string" || typeof value === "boolean") return value;
    if (typeof value === "number" && Number.isFinite(value)) return value;
    if (typeof value !== "object" || value === null || parents.has(value) ||
      (!Array.isArray(value) && Object.getPrototypeOf(value) !== Object.prototype && Object.getPrototypeOf(value) !== null)) {
      throw new CommandError("invalid_params", "Параметры команды должны быть JSON-данными.");
    }
    parents.add(value);
    const result = Array.isArray(value) ? value.map(copy) : Object.fromEntries(Object.entries(value).map(([key, item]) => [key, copy(item)]));
    parents.delete(value);
    return Object.freeze(result);
  }
  if (params === null || typeof params !== "object" || Array.isArray(params)) {
    throw new CommandError("invalid_params", "Параметры команды должны быть объектом.");
  }
  return copy(params) as JsonObject;
}

export type CommandAvailability = { readonly enabled: true } | { readonly enabled: false; readonly reason: string };
export const CANCEL_COMMAND = Symbol("cancel command preparation");
export interface CommandDefinition {
  available(context: CommandContext): CommandAvailability;
  prepare?(context: CommandContext, params: JsonObject): JsonObject | typeof CANCEL_COMMAND | PromiseLike<JsonObject | typeof CANCEL_COMMAND>;
  execute(request: CommandRequest): void | PromiseLike<void>;
}

export class CommandDispatcher<Id extends string = CommandId> {
  private readonly definitions = new Map<Id, CommandDefinition>();
  private readonly readContext: () => CommandContext;
  private readonly newId: () => string;

  constructor(readContext: () => CommandContext, newId: () => string = () => crypto.randomUUID()) {
    this.readContext = readContext;
    this.newId = newId;
  }

  register(id: Id, definition: CommandDefinition): void {
    if (this.definitions.has(id)) throw new CommandError("duplicate_command", `Команда уже зарегистрирована: ${id}`);
    this.definitions.set(id, Object.freeze({ ...definition }));
  }

  availability(id: Id): CommandAvailability {
    const definition = this.definitions.get(id);
    if (!definition) return { enabled: false, reason: `Неизвестная команда: ${id}` };
    return definition.available(snapshotContext(this.readContext()));
  }

  async dispatch(id: Id, input: JsonObject = {}): Promise<"executed" | "cancelled"> {
    const definition = this.definitions.get(id);
    if (!definition) throw new CommandError("unknown_command", `Неизвестная команда: ${id}`);
    try {
      // Capture once, before availability and any asynchronous parameter dialog.
      const context = snapshotContext(this.readContext());
      const initialParams = snapshotParams(input);
      const availability = definition.available(context);
      if (!availability.enabled) throw new CommandError("unavailable", availability.reason);
      const invocationId = this.newId();
      const params = definition.prepare ? await definition.prepare(context, initialParams) : initialParams;
      if (params === CANCEL_COMMAND) return "cancelled";
      await definition.execute(Object.freeze({ id: invocationId, type: id, context, params: snapshotParams(params) }));
      return "executed";
    } catch (error) {
      if (error instanceof CommandError) throw error;
      throw new CommandError("handler_failed", `Не удалось выполнить команду: ${commandErrorMessage(error)}`, error);
    }
  }
}
