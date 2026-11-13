export type ParsedCommand =
  | { readonly kind: "empty" }
  | { readonly kind: "home" }
  | { readonly kind: "navigate"; readonly path: string }
  | { readonly kind: "external"; readonly command: string }
  | { readonly kind: "error"; readonly message: string };

const unsafeShellSyntax = /[$`;&|<>\0\r\n]/;

function parsePath(source: string): ParsedCommand {
  if (unsafeShellSyntax.test(source)) {
    return { kind: "error", message: "Подстановки и операторы shell в cd не поддерживаются." };
  }
  const quote = source[0];
  if (quote !== '"' && quote !== "'") {
    if (/\s/.test(source)) return { kind: "error", message: "Путь с пробелами нужно заключить в кавычки." };
    return { kind: "navigate", path: source };
  }
  let path = "";
  for (let index = 1; index < source.length; index++) {
    const character = source[index];
    if (character === quote) {
      if (source.slice(index + 1).trim() !== "") {
        return { kind: "error", message: "После закрывающей кавычки в cd не должно быть других аргументов." };
      }
      return path === "" ? { kind: "error", message: "Путь для cd не может быть пустым." }
        : { kind: "navigate", path };
    }
    if (character === "\\" && source[index + 1] === quote) {
      path += source[++index];
    } else {
      path += character;
    }
  }
  return { kind: "error", message: "В команде cd не закрыта кавычка." };
}

export function parseCommandLine(input: string): ParsedCommand {
  const command = input.trim();
  if (command === "") return { kind: "empty" };
  const separator = command.search(/\s/);
  const name = separator < 0 ? command : command.slice(0, separator);
  if (name !== "cd") return { kind: "external", command: input };
  const argument = separator < 0 ? "" : command.slice(separator).trim();
  return argument === "" ? { kind: "home" } : parsePath(argument);
}
