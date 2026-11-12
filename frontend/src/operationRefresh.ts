import type { Operation } from "../bindings/github.com/rikkimongoose/marten-commander/internal/operations/models.ts";

// Terminal move states, including failed/cancelled partial results, refresh
// both captured directories. Panels that navigated elsewhere ignore the opaque
// locations in FilePanel.refreshDirectory.
export function operationRefreshLocations(operation: Operation): readonly string[] {
  if (operation.type !== "file.copy" && operation.type !== "file.move") return [];
  const values: string[] = [];
  if (operation.context.destinationDirectory) values.push(operation.context.destinationDirectory);
  if (operation.type === "file.move") {
    try {
      const destination = JSON.parse(operation.context.parametersJSON)?.destination;
      if (typeof destination === "string" && destination.startsWith("/")) values.push(encodeLocation(destination));
    } catch { /* The backend owns validation; the captured parent still refreshes. */ }
  }
  if (operation.type === "file.move" && operation.context.sourceDirectory) values.push(operation.context.sourceDirectory);
  return [...new Set(values)];
}

export function moveFocusCandidates(operation: Operation): readonly { directoryLocation: string; name: string }[] {
  const sources = operation.context.sources ?? [];
  if (operation.type !== "file.move" || sources.length !== 1) return [];
  try {
    const destination = JSON.parse(operation.context.parametersJSON)?.destination;
    if (typeof destination !== "string" || !destination.startsWith("/") || !operation.context.destinationDirectory) return [];
    const targetParts = destination.split("/").filter(Boolean);
    const sourceParts = decodeLocation(sources[0]).split("/").filter(Boolean);
    const targetName = targetParts[targetParts.length - 1];
    const sourceName = sourceParts[sourceParts.length - 1];
    const result: { directoryLocation: string; name: string }[] = [];
    if (targetName) result.push({ directoryLocation: operation.context.destinationDirectory, name: targetName });
    if (sourceName) result.push({ directoryLocation: encodeLocation(destination), name: sourceName });
    return result;
  } catch { return []; }
}

function encodeLocation(value: string): string {
  const bytes = new TextEncoder().encode(value);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function decodeLocation(value: string): string {
  const base64 = value.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(value.length / 4) * 4, "=");
  const binary = atob(base64);
  return new TextDecoder("utf-8", { fatal: true }).decode(Uint8Array.from(binary, character => character.charCodeAt(0)));
}
