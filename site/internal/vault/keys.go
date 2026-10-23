// Package vault keeps a user's folder encrypted on disk, so whoever runs the server can't read
// it while the user is away. A random key per user (the data key) encrypts every file. The data
// key itself is stored only wrapped: once with the user's password, once with their recovery
// code, and once per signed-in browser session and plugin token, so any of them can unlock it
// and none of them is kept by the server. (The "password" and "recovery code" here are proofs
// the browser makes of them; the real ones stay in the browser, see web/js/keys.js.)
//
// While the user is signed in the folder is decrypted into memory (see Mirror) for the
// dashboard and its tools to use, and written back encrypted as it changes.
//
// What this doesn't do: stop someone who controls the running server from reading this data
// while the user is signed in (they can read its memory). Notes are encrypted end to end in the
// browser instead, so the server never has them readable.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"dashboard/internal/jsonfile"
	"dashboard/internal/problem"
)

// passwordIterations is PBKDF2-SHA256's count for the password's key, as for login (auth).
const passwordIterations = 600_000

// ErrWrongSecret is returned when nothing stored opens with the secret given.
var ErrWrongSecret error = problem.Error{Kind: problem.Invalid, Message: "That doesn't unlock this account's data"}

// Kinds of secret that can unlock the data key.
const (
	Password = "password"
	Recovery = "recovery"
	Session  = "session" // a browser's session cookie
	Token    = "token"   // the bar plugin's token
)

// Key is a user's data key, in memory only while their data is unlocked.
type Key struct {
	userID string
	data   []byte
}

// slot is the data key wrapped with one secret.
type slot struct {
	Kind       string `json:"kind"`
	Salt       string `json:"salt,omitempty"` // password only
	Iterations int    `json:"iterations,omitempty"`
	Wrapped    string `json:"wrapped"` // nonce + AES-GCM sealed data key
}

// keyring is keys.json: the slots by name ("password", "recovery", "session:<fingerprint>"...).
type keyring struct {
	Version int             `json:"version"`
	Slots   map[string]slot `json:"slots"`
}

// Keys is a user's keyring file.
type Keys struct {
	file, userID string
	mu           sync.Mutex
}

func NewKeys(dir, userID string) *Keys {
	return &Keys{file: filepath.Join(dir, "keys.json"), userID: userID}
}

// Exists reports whether the user has encrypted data.
func (k *Keys) Exists() bool {
	_, err := os.Stat(k.file)
	return err == nil
}

// Create makes a new data key, wrapped with the password and the recovery code (the proofs the
// browser makes of them: it keeps the real ones), and returns it. It replaces any keyring
// already there, whose data can then never be opened.
func (k *Keys) Create(password, recovery string) (*Key, error) {
	if password == "" || recovery == "" {
		return nil, problem.Invalidf("A new keyring needs a password and a recovery code")
	}
	data := make([]byte, 32)
	rand.Read(data)
	key := &Key{userID: k.userID, data: data}
	ring := keyring{Version: 1, Slots: map[string]slot{}}
	for kind, secret := range map[string]string{Password: password, Recovery: recovery} {
		s, err := k.reseal(key, newSlot(kind), kind, secret)
		if err != nil {
			return nil, err
		}
		ring.Slots[kind] = s
	}
	if err := os.MkdirAll(filepath.Dir(k.file), 0o700); err != nil {
		return nil, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return key, jsonfile.Write(k.file, ring)
}

// Open unwraps the data key with a secret of the given kind. Sessions and tokens are found by
// their fingerprint (name), the password and recovery code by their kind.
func (k *Keys) Open(kind, name, secret string) (*Key, error) {
	ring, err := k.read()
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = kind
	}
	s, found := ring.Slots[name]
	if !found || s.Kind != kind {
		return nil, ErrWrongSecret
	}
	kek, err := k.kek(s, secret)
	if err != nil {
		return nil, err
	}
	sealed, err := base64.StdEncoding.DecodeString(s.Wrapped)
	if err != nil {
		return nil, ErrWrongSecret
	}
	data, err := open(kek, sealed, k.label(name))
	if err != nil {
		return nil, ErrWrongSecret
	}
	return &Key{userID: k.userID, data: data}, nil
}

// Add stores the data key wrapped with another secret under name (replacing what was there).
func (k *Keys) Add(key *Key, kind, name, secret string) error {
	if name == "" {
		name = kind
	}
	// The wrap is sealed for its name, so it can't be moved to another slot.
	s, err := k.reseal(key, newSlot(kind), name, secret)
	if err != nil {
		return err
	}
	return k.update(func(ring *keyring) { ring.Slots[name] = s })
}

// Remove drops the slots for which drop is true.
func (k *Keys) Remove(drop func(name string, kind string) bool) error {
	return k.update(func(ring *keyring) {
		for name, s := range ring.Slots {
			if drop(name, s.Kind) {
				delete(ring.Slots, name)
			}
		}
	})
}

// Names lists the slots of a kind.
func (k *Keys) Names(kind string) []string {
	ring, err := k.read()
	if err != nil {
		return nil
	}
	var names []string
	for name, s := range ring.Slots {
		if s.Kind == kind {
			names = append(names, name)
		}
	}
	return names
}

func (k *Keys) read() (keyring, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	var ring keyring
	if !jsonfile.Read(k.file, &ring) || ring.Slots == nil {
		return keyring{}, ErrWrongSecret
	}
	return ring, nil
}

func (k *Keys) update(change func(*keyring)) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	var ring keyring
	if !jsonfile.Read(k.file, &ring) || ring.Slots == nil {
		return problem.NotFoundf("This account has no encrypted data")
	}
	change(&ring)
	return jsonfile.Write(k.file, ring)
}

// label binds a wrap to its user and slot.
func (k *Keys) label(name string) []byte {
	return []byte("dashboard-vault/v1/" + k.userID + "/key/" + name)
}

// newSlot is an empty slot of a kind; a password's gets a fresh salt.
func newSlot(kind string) slot {
	if kind != Password {
		return slot{Kind: kind}
	}
	salt := make([]byte, 16)
	rand.Read(salt)
	return slot{Kind: Password, Salt: base64.StdEncoding.EncodeToString(salt), Iterations: passwordIterations}
}

// reseal seals the data key into s for the slot called name.
func (k *Keys) reseal(key *Key, s slot, name, secret string) (slot, error) {
	kek, err := k.kek(s, secret)
	if err != nil {
		return slot{}, err
	}
	s.Wrapped = base64.StdEncoding.EncodeToString(seal(kek, key.data, k.label(name)))
	return s, nil
}

// kek is the key that wraps the data key in s: PBKDF2 for a password (slow, since passwords can
// be guessed), HKDF for the random secrets (recovery code, session, token), which can't be.
func (k *Keys) kek(s slot, secret string) ([]byte, error) {
	if secret == "" {
		return nil, ErrWrongSecret
	}
	if s.Kind == Password {
		salt, err := base64.StdEncoding.DecodeString(s.Salt)
		if err != nil || s.Iterations < 1 {
			return nil, ErrWrongSecret
		}
		return pbkdf2.Key(sha256.New, secret, salt, s.Iterations, 32)
	}
	return hkdf.Key(sha256.New, []byte(secret), []byte(k.userID), "dashboard vault "+s.Kind, 32)
}

// ---------- AES-GCM ----------

func seal(key, plain, label []byte) []byte {
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	nonce := make([]byte, aead.NonceSize())
	rand.Read(nonce)
	return aead.Seal(nonce, nonce, plain, label)
}

func open(key, sealed, label []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < aead.NonceSize() {
		return nil, errors.New("damaged")
	}
	return aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], label)
}
