package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

const RefreshTokenBytes = 32

// NewRefreshToken returns the raw token (given to the client, never stored)
// and its SHA-256 hash (stored in refresh_tokens.token_hash) — losing the
// database doesn't hand out usable refresh tokens.
func NewRefreshToken() (raw string, hash string, err error) {
	buf := make([]byte, RefreshTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = hex.EncodeToString(buf)
	return raw, HashRefreshToken(raw), nil
}

func HashRefreshToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
