// Package secrets read the AI keys users once kept on the server (sealed with site/data/
// secret.key), from before keys moved into their browser: the browser takes them over once and
// the server clears them (web/sealed.go). Nothing new is stored here.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"dashboard/internal/jsonfile"
)

// Box seals and opens secrets with one AES-256-GCM key.
type Box struct{ aead cipher.AEAD }

// LoadBox reads the key file, making a new random key the first time.
func LoadBox(keyFile string) (*Box, error) {
	key, err := os.ReadFile(keyFile)
	if os.IsNotExist(err) {
		key = make([]byte, 32)
		rand.Read(key)
		if err := os.WriteFile(keyFile, key, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("%s should hold a 32-byte key", keyFile)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plain; label (e.g. the user id and the name) is bound in, so a sealed value
// copied to another user or name doesn't open.
func (b *Box) Seal(plain, label string) string {
	nonce := make([]byte, b.aead.NonceSize())
	rand.Read(nonce)
	return base64.StdEncoding.EncodeToString(b.aead.Seal(nonce, nonce, []byte(plain), []byte(label)))
}

func (b *Box) Open(sealed, label string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(data) < b.aead.NonceSize() {
		return "", errors.New("damaged secret")
	}
	plain, err := b.aead.Open(nil, data[:b.aead.NonceSize()], data[b.aead.NonceSize():], []byte(label))
	if err != nil {
		return "", errors.New("damaged secret")
	}
	return string(plain), nil
}

// Store is one user's sealed secrets by name, in a JSON file in their folder.
type Store struct {
	box   *Box
	file  string
	owner string
	mu    sync.Mutex
}

func NewStore(box *Box, dir, owner string) *Store {
	return &Store{box: box, file: filepath.Join(dir, "secrets.json"), owner: owner}
}

func (s *Store) read() map[string]string {
	sealed := map[string]string{}
	jsonfile.Read(s.file, &sealed)
	return sealed
}

// Get is the secret called name ("" when there's none or it doesn't open).
func (s *Store) Get(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	sealed, ok := s.read()[name]
	if !ok {
		return ""
	}
	plain, err := s.box.Open(sealed, s.owner+"/"+name)
	if err != nil {
		return ""
	}
	return plain
}

func (s *Store) Has(name string) bool { return s.Get(name) != "" }

// Set saves the secret; an empty value removes it.
func (s *Store) Set(name, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sealed := s.read()
	if value == "" {
		delete(sealed, name)
	} else {
		sealed[name] = s.box.Seal(value, s.owner+"/"+name)
	}
	return jsonfile.Write(s.file, sealed)
}

// Clear forgets every secret (the file goes).
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.file)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
