package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const AccessTokenTTL = 15 * time.Minute

type Claims struct {
	ProfileID string `json:"sub"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	FullName  string `json:"full_name,omitempty"`
	Phone     string `json:"phone,omitempty"`
	jwt.RegisteredClaims
}

// GenerateAccessToken embeds full_name/phone alongside the usual identity
// claims — low-churn profile data, worth denormalizing into the token so
// pages that want it (e.g. checkout prefill) don't need an extra round
// trip. The token is re-issued every 15 minutes, so staleness is bounded.
func GenerateAccessToken(secret []byte, profileID, email, role, fullName, phone string) (string, error) {
	now := time.Now()
	claims := Claims{
		ProfileID: profileID,
		Email:     email,
		Role:      role,
		FullName:  fullName,
		Phone:     phone,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenTTL)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

// VerifyAccessToken checks the signature and expiry locally — no database
// or network round trip. This is the fix for the 2-3 Supabase Auth network
// calls hooks.server.js made on every SSR request.
func VerifyAccessToken(secret []byte, tokenString string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}
