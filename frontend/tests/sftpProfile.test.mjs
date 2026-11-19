import test from "node:test";
import assert from "node:assert/strict";
import { emptySFTPProfile, profileForSave, sftpProfileError } from "../src/sftpProfile.ts";

test("SFTP profile validation accepts Unicode names, hostnames, IPv6 and POSIX paths", () => {
  const profile = { ...emptySFTPProfile(), name: "  Рабочий сервер  ", host: "сервер.example", port: 2222,
    username: "пользователь", authMethod: "password", remotePath: "/данные/проекты", timeoutSeconds: 30 };
  assert.equal(sftpProfileError(profile), "");
  assert.equal(sftpProfileError({ ...profile, host: "2001:db8::1" }), "");
});

test("SFTP profile validation rejects unsafe and incomplete fields", () => {
  const profile = { ...emptySFTPProfile(), name: "server", host: "host", username: "user" };
  assert.match(sftpProfileError({ ...profile, port: 0 }), /Порт/);
  assert.match(sftpProfileError({ ...profile, host: "bad host" }), /hostname/);
  assert.match(sftpProfileError({ ...profile, username: "" }), /пользователя/);
  assert.match(sftpProfileError({ ...profile, authMethod: "key", privateKeyPath: "" }), /ключу/);
  assert.match(sftpProfileError({ ...profile, remotePath: "C:\\local" }), /POSIX/);
});

test("switching away from key removes its local path but never introduces secrets", () => {
  const profile = { ...emptySFTPProfile(), name: "server", host: " host ", username: " user ", authMethod: "password",
    privateKeyPath: "/home/user/.ssh/id_ed25519" };
  const saved = profileForSave(profile);
  assert.equal(saved.privateKeyPath, "");
  assert.deepEqual(Object.keys(saved).filter(key => /password|passphrase|secret|token/i.test(key)), []);
});
