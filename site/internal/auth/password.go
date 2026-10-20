package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"dashboard/internal/problem"
)

// Passwords are stretched with PBKDF2-HMAC-SHA256 at OWASP's 2023 count, so each guess at a
// stolen account.json costs about as much as a login.
const (
	pbkdf2Iterations = 600_000
	saltBytes        = 16
	hashBytes        = 32

	minPasswordLength = 12
	maxPasswordLength = 256
	maxUsernameLength = 32
)

type passwordHash struct {
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
	Iterations int    `json:"iterations"`
}

func hashPassword(password string) (passwordHash, error) {
	salt := make([]byte, saltBytes)
	rand.Read(salt)
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, hashBytes)
	if err != nil {
		return passwordHash{}, err
	}
	return passwordHash{
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Hash:       base64.StdEncoding.EncodeToString(key),
		Iterations: pbkdf2Iterations,
	}, nil
}

// matches compares in constant time. A hash it can't read never matches.
func (p passwordHash) matches(password string) bool {
	salt, saltErr := base64.StdEncoding.DecodeString(p.Salt)
	want, hashErr := base64.StdEncoding.DecodeString(p.Hash)
	if saltErr != nil || hashErr != nil || len(want) == 0 || p.Iterations < 1 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, p.Iterations, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash is checked when the username is wrong, so a wrong name takes as long as a wrong
// password and the timing doesn't tell which one it was.
var dummyHash, _ = hashPassword("not the password")

func checkUsername(name string) error {
	if name == "" || len(name) > maxUsernameLength {
		return problem.Invalidf("Pick a username of 1 to 32 characters")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return problem.Invalidf("Usernames can only have letters, numbers, dots, dashes and underscores")
		}
	}
	return nil
}

func checkNewPassword(username, password, confirm string) error {
	length := utf8.RuneCountInString(password)
	switch {
	case length < minPasswordLength:
		return problem.Invalidf("Use at least 12 characters for your password")
	case length > maxPasswordLength:
		return problem.Invalidf("Use at most 256 characters for your password")
	case strings.EqualFold(password, username):
		return problem.Invalidf("Your password can't be your username")
	case strings.Count(password, password[:1]) == len(password):
		return problem.Invalidf("Your password can't be one character repeated")
	case password != confirm:
		return problem.Invalidf("The two passwords don't match")
	}
	return nil
}

// newSecret is 32 random bytes as URL-safe text: session cookies, the plugin token, the setup code.
func newSecret() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// fingerprint is what's stored for a session or token, so the files never hold one that works.
func fingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
