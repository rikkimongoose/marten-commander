export type ViewerMode = "text" | "hex" | "image" | "unknown";

export const viewerModes: readonly { id: ViewerMode; label: string; available: boolean; note?: string }[] = Object.freeze([
  { id: "text", label: "Текст", available: true },
  { id: "hex", label: "HEX", available: true },
  { id: "image", label: "Изображение", available: false, note: "Будет реализовано в Issue #23" },
]);

export function normalizeViewerMode(value: string): ViewerMode {
  return value === "text" || value === "hex" || value === "image" ? value : "unknown";
}

export function viewerEscapeAction(key: string, menuOpen: boolean): "none" | "closeMenu" | "closeViewer" {
  if (key !== "Escape") return "none";
  return menuOpen ? "closeMenu" : "closeViewer";
}

export function decodeBase64(value: string): Uint8Array {
  const binary = atob(value);
  return Uint8Array.from(binary, character => character.charCodeAt(0));
}

export function textPreview(bytes: Uint8Array): string {
  return new TextDecoder("utf-8", { fatal: false }).decode(bytes);
}

export function hexPreview(bytes: Uint8Array, start = 0n): string {
  const rows: string[] = [];
  for (let index = 0; index < bytes.length; index += 16) {
    const row = bytes.slice(index, index + 16);
    const address = (start + BigInt(index)).toString(16).padStart(8, "0");
    const hex = [...row].map(value => value.toString(16).padStart(2, "0")).join(" ").padEnd(47);
    const ascii = [...row].map(value => value >= 32 && value < 127 ? String.fromCharCode(value) : ".").join("");
    rows.push(address + "  " + hex + "  |" + ascii + "|");
  }
  return rows.join("\n");
}
