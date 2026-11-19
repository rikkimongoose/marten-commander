import type { ConnectionProfile } from "../bindings/github.com/rikkimongoose/marten-commander/internal/sftp/models";

export const emptySFTPProfile = (): ConnectionProfile => ({
  id: "", name: "", host: "", port: 22, username: "", authMethod: "agent" as ConnectionProfile["authMethod"],
  privateKeyPath: "", remotePath: "", timeoutSeconds: 15,
});

export function sftpProfileError(profile: ConnectionProfile): string {
  if (!profile.name.trim() || /[\u0000-\u001f\u007f]/u.test(profile.name)) return "Укажите название профиля.";
  const host = profile.host.trim();
  if (!host || /[\s/@\u0000-\u001f\u007f]/u.test(host)) return "Укажите корректный hostname или IP-адрес.";
  if (!Number.isInteger(profile.port) || profile.port < 1 || profile.port > 65535) return "Порт должен быть в диапазоне 1–65535.";
  if (!profile.username.trim() || /[\u0000-\u001f\u007f]/u.test(profile.username)) return "Укажите имя пользователя.";
  if (!(["agent", "key", "password"] as string[]).includes(profile.authMethod)) return "Выберите способ аутентификации.";
  if (profile.authMethod === "key" && !profile.privateKeyPath?.trim()) return "Укажите путь к приватному SSH-ключу.";
  if (profile.remotePath && (!profile.remotePath.startsWith("/") || /[\u0000-\u001f\u007f]/u.test(profile.remotePath))) {
    return "Начальный каталог должен быть абсолютным POSIX-путём.";
  }
  const timeout = profile.timeoutSeconds ?? 15;
  if (!Number.isInteger(timeout) || timeout < 1 || timeout > 300) return "Таймаут должен быть в диапазоне 1–300 секунд.";
  return "";
}

export function profileForSave(profile: ConnectionProfile): ConnectionProfile {
  return { ...profile, host: profile.host.trim(), username: profile.username.trim(),
    privateKeyPath: profile.authMethod === "key" ? (profile.privateKeyPath ?? "").trim() : "" };
}
