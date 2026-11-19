package sftp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validProfile(name string) ConnectionProfile {
	return ConnectionProfile{Name: name, Host: "сервер.example", Port: 22, Username: "user", AuthMethod: AuthPassword,
		RemotePath: "/home/user", TimeoutSeconds: 15}
}

func TestProfileStoreCreateUpdateDeleteAndReload(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "config", "marten-commander")
	store := NewProfileStore(directory)
	first, err := store.Save(validProfile("Основной сервер"))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" {
		t.Fatal("stable id was not generated")
	}
	secondProfile := validProfile("Другой пользователь")
	secondProfile.Username = "other"
	second, err := store.Save(secondProfile)
	if err != nil {
		t.Fatal(err)
	}
	first.RemotePath = "/srv/data"
	if _, err := store.Save(first); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewProfileStore(directory).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded) != 2 {
		t.Fatalf("got %d profiles, want 2", len(reloaded))
	}
	updated, err := store.Get(first.ID)
	if err != nil || updated.RemotePath != "/srv/data" {
		t.Fatalf("updated profile was not persisted: %#v, %v", updated, err)
	}
	if err := store.Delete(first.ID); err != nil {
		t.Fatal(err)
	}
	remaining, err := NewProfileStore(directory).List()
	if err != nil || len(remaining) != 1 || remaining[0].ID != second.ID {
		t.Fatalf("delete affected another profile: %#v, %v", remaining, err)
	}
}

func TestProfileStorePermissionsAtomicShapeAndNoSecrets(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "nested", "config")
	store := NewProfileStore(directory)
	profile := validProfile("Пароль не хранится")
	if _, err := store.Save(profile); err != nil {
		t.Fatal(err)
	}
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if directoryInfo.Mode().Perm() != 0o700 || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("permissions are directory=%o file=%o", directoryInfo.Mode().Perm(), fileInfo.Mode().Perm())
	}
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, forbidden := range []string{`"password":`, `"passphrase":`, `"secret":`, `"privateKey":`, `"token":`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("serialized profile contains forbidden field %q: %s", forbidden, text)
		}
	}
	matches, err := filepath.Glob(filepath.Join(directory, ".sftp-profiles-*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("atomic temporary files leaked: %v, %v", matches, err)
	}
}

func TestProfileStoreRejectsInvalidAndDamagedData(t *testing.T) {
	store := NewProfileStore(t.TempDir())
	tests := []ConnectionProfile{
		{Name: "", Host: "host", Port: 22, Username: "user", AuthMethod: AuthAgent},
		{Name: "name", Host: "bad host", Port: 22, Username: "user", AuthMethod: AuthAgent},
		{Name: "name", Host: "host", Port: 70000, Username: "user", AuthMethod: AuthAgent},
		{Name: "name", Host: "host", Port: 22, Username: "", AuthMethod: AuthAgent},
		{Name: "name", Host: "host", Port: 22, Username: "user", AuthMethod: AuthKey},
		{Name: "name", Host: "host", Port: 22, Username: "user", AuthMethod: AuthPassword, RemotePath: `C:\\local`},
	}
	for _, profile := range tests {
		if _, err := store.Save(profile); err == nil {
			t.Fatalf("invalid profile was accepted: %#v", profile)
		}
	}
	for _, damaged := range []string{"", "{", `{"version":1,"profiles":[],"secret":"leak"}`, `{"version":99,"profiles":[]}`} {
		if err := os.WriteFile(store.Path(), []byte(damaged), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.List(); err == nil {
			// The exact JSON parser message is intentionally not part of the API;
			// every malformed document must simply remain a visible error.
			t.Fatalf("damaged document was silently accepted: %q", damaged)
		}
	}
}

func TestNetworkHostSupportsIPv4IPv6AndUnicode(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "2001:db8::1", "[2001:db8::1]", "сервер.example"} {
		if _, err := NetworkHost(host); err != nil {
			t.Errorf("%q rejected: %v", host, err)
		}
	}
}
