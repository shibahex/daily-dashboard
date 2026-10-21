package vault

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeys(t *testing.T) {
	keys := NewKeys(t.TempDir(), "user1")
	if keys.Exists() {
		t.Fatal("no keyring yet")
	}
	key, err := keys.Create("correct horse", "recovery-proof")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Open(Recovery, "", "RECOVERY-PROOF"); err == nil {
		t.Error("the recovery proof is taken as it is (the browser cleans up what was typed)")
	}

	for _, c := range []struct{ kind, name, secret string }{
		{Password, "", "correct horse"},
		{Recovery, "", "recovery-proof"},
	} {
		got, err := keys.Open(c.kind, c.name, c.secret)
		if err != nil || !bytes.Equal(got.data, key.data) {
			t.Errorf("%s: %v", c.kind, err)
		}
	}
	if _, err := keys.Open(Password, "", "wrong"); err != ErrWrongSecret {
		t.Errorf("a wrong password: %v", err)
	}

	// A session's wrap opens only under its own name.
	if err := keys.Add(key, Session, "session:aa", "cookie-secret"); err != nil {
		t.Fatal(err)
	}
	if got, err := keys.Open(Session, "session:aa", "cookie-secret"); err != nil || !bytes.Equal(got.data, key.data) {
		t.Errorf("session: %v", err)
	}
	if _, err := keys.Open(Session, "session:bb", "cookie-secret"); err == nil {
		t.Error("an unknown session opened")
	}

	// A new password replaces the old one.
	if err := keys.Add(key, Password, "", "new password"); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Open(Password, "", "correct horse"); err == nil {
		t.Error("the old password still opens")
	}
	if err := keys.Remove(func(name, kind string) bool { return kind == Session }); err != nil {
		t.Fatal(err)
	}
	if len(keys.Names(Session)) != 0 {
		t.Error("sessions not removed")
	}

	// Another user's keyring copied over doesn't open: wraps are bound to the user.
	other := NewKeys(filepath.Dir(keys.file), "user2")
	if _, err := other.Open(Password, "", "new password"); err == nil {
		t.Error("another user's keyring opened")
	}
}

func TestMirror(t *testing.T) {
	keys := NewKeys(t.TempDir(), "user1")
	key, _ := keys.Create("pw", "rc")
	encrypted := filepath.Join(t.TempDir(), "vault")
	plain := filepath.Join(t.TempDir(), "plain")
	write := func(path, text string) {
		os.MkdirAll(filepath.Dir(filepath.Join(plain, path)), 0o700)
		os.WriteFile(filepath.Join(plain, path), []byte(text), 0o600)
	}
	write("plan.json", `{"goals":[]}`)
	write("docs/secret-diary.md", "dear diary")
	write("mfp/data/browser-profile/Default/Cache/junk", "cache")
	os.MkdirAll(filepath.Join(plain, "task"), 0o700)
	os.Symlink("/somewhere/hook", filepath.Join(plain, "hook"))

	m := NewMirror(encrypted, plain, key)
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	// Nothing readable on disk: no names, no contents.
	filepath.WalkDir(encrypted, func(path string, d os.DirEntry, err error) error {
		if strings.Contains(path, "diary") || strings.Contains(path, "plan") {
			t.Errorf("a name leaked: %s", path)
		}
		if data, err := os.ReadFile(path); err == nil && bytes.Contains(data, []byte("dear diary")) {
			t.Errorf("contents leaked in %s", path)
		}
		return nil
	})
	blobs, _ := os.ReadDir(filepath.Join(encrypted, "blobs"))
	if len(blobs) != 2 {
		t.Errorf("want 2 blobs (cache skipped), got %d", len(blobs))
	}

	// Change one file, delete another: one new blob, the old ones gone.
	write("plan.json", `{"goals":[1]}`)
	os.Remove(filepath.Join(plain, "docs/secret-diary.md"))
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	blobs, _ = os.ReadDir(filepath.Join(encrypted, "blobs"))
	if len(blobs) != 1 {
		t.Errorf("want 1 blob after the change, got %d", len(blobs))
	}

	// Restore into a fresh folder with the key.
	restored := filepath.Join(t.TempDir(), "again")
	again := NewMirror(encrypted, restored, key)
	if err := again.Restore(); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(restored, "plan.json")); string(data) != `{"goals":[1]}` {
		t.Errorf("plan.json = %q", data)
	}
	if link, _ := os.Readlink(filepath.Join(restored, "hook")); link != "/somewhere/hook" {
		t.Errorf("symlink = %q", link)
	}
	if info, err := os.Stat(filepath.Join(restored, "task")); err != nil || !info.IsDir() {
		t.Error("empty folder not restored")
	}
	// Restored files keep their times, so saving again writes nothing new.
	before, _ := os.ReadDir(filepath.Join(encrypted, "blobs"))
	if err := again.Save(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadDir(filepath.Join(encrypted, "blobs"))
	if len(before) != 1 || len(after) != 1 || before[0].Name() != after[0].Name() {
		t.Error("an unchanged folder was re-encrypted")
	}

	// A wrong key can't restore.
	wrong, _ := NewKeys(t.TempDir(), "user1").Create("x", "y")
	if err := NewMirror(encrypted, filepath.Join(t.TempDir(), "x"), wrong).Restore(); err == nil {
		t.Error("restored with the wrong key")
	}

	if err := Wipe(restored); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(restored); !os.IsNotExist(err) {
		t.Error("not wiped")
	}
}
