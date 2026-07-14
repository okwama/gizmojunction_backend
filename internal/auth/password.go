package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// Argon2id params tuned for an interactive login path (not a batch job) —
// OWASP's current minimum recommendation for this profile.
const (
	argonTime    = 1
	argonMemory  = 64 * 1024 // 64MB
	argonThreads = 4
	argonKeyLen  = 32
)

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$%d$%d$%d$%s$%s",
		argonTime, argonMemory, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

func VerifyArgon2id(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "argon2id" {
		return false, fmt.Errorf("invalid argon2id hash format")
	}
	var time, memory uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[1], "%d", &time); err != nil {
		return false, err
	}
	if _, err := fmt.Sscanf(parts[2], "%d", &memory); err != nil {
		return false, err
	}
	if _, err := fmt.Sscanf(parts[3], "%d", &threads); err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// VerifyBcrypt supports the legacy path: a profile migrated from a real
// Supabase export would have password_algo='bcrypt' and its original
// GoTrue-issued hash in password_hash. There's no real exported password
// data to migrate yet, so this is built and tested against synthetic
// bcrypt hashes only.
func VerifyBcrypt(password, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
