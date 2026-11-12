import type { CommandRequest as WailsCommandRequest } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/models";
import { CommandError, type CommandRequest } from "./commands.ts";

type ExecuteBinding = (request: WailsCommandRequest) => PromiseLike<void>;

// Lazy loading keeps frontend-only commands independent of the Wails runtime.
// Tests inject a binding; production always uses the generated service method.
export function createCommandBackend(binding?: ExecuteBinding) {
  return async (request: CommandRequest): Promise<void> => {
    const execute = binding ?? (await import("../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/commandservice")).ExecuteCommand;
    try {
      // The generated DTO has mutable arrays. Send a separate transport copy.
      await execute({ ...request, context: {
        activePanel: request.context.activePanel,
        left: { ...request.context.left, selectedLocations: [...request.context.left.selectedLocations] },
        right: { ...request.context.right, selectedLocations: [...request.context.right.selectedLocations] },
      } });
    } catch (error) {
      // Wails RuntimeError exposes the JSON-marshalled Go error as its cause.
      const cause = error instanceof Error && "cause" in error ? error.cause : undefined;
      if (cause && typeof cause === "object" && "code" in cause && "message" in cause && typeof cause.message === "string" &&
        (cause.code === "unknown_command" || cause.code === "invalid_request" || cause.code === "invalid_context" || cause.code === "invalid_params" ||
          cause.code === "unavailable" || cause.code === "open_failed" || cause.code === "mkdir_failed" || cause.code === "copy_failed" || cause.code === "move_failed")) {
        throw new CommandError(cause.code, cause.message, error);
      }
      throw error;
    }
  };
}
