package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretsAreSealedPerUser(t *testing.T) {
	dir := t.TempDir()
	box, err := LoadBox(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	alice := NewStore(box, filepath.Join(dir, "alice"), "alice")
	os.MkdirAll(filepath.Join(dir, "alice"), 0o700)
	if err := alice.Set("anthropic", "sk-ant-secret"); err != nil {
		t.Fatal(err)
	}
	if alice.Get("anthropic") != "sk-ant-secret" || !alice.Has("anthropic") || alice.Has("openai") {
		t.Fatal("round trip failed")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "alice", "secrets.json"))
	if strings.Contains(string(data), "sk-ant-secret") {
		t.Fatal("the file holds the key in the clear")
	}

	// Alice's file copied into Bob's folder doesn't open for Bob.
	os.MkdirAll(filepath.Join(dir, "bob"), 0o700)
	os.WriteFile(filepath.Join(dir, "bob", "secrets.json"), data, 0o600)
	if NewStore(box, filepath.Join(dir, "bob"), "bob").Get("anthropic") != "" {
		t.Fatal("a sealed secret must only open for its owner")
	}

	again, _ := LoadBox(filepath.Join(dir, "secret.key"))
	if NewStore(again, filepath.Join(dir, "alice"), "alice").Get("anthropic") != "sk-ant-secret" {
		t.Fatal("the key should survive a restart")
	}
	alice.Set("anthropic", "")
	if alice.Has("anthropic") {
		t.Fatal("an empty value removes the secret")
	}
}
