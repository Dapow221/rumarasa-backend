package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	AccessTokenTTL  = 15 * time.Minute
	RefreshTokenTTL = 30 * 24 * time.Hour
	BcryptCost      = 12
)

// NewAccessToken issues a short-lived HS256 JWT for an admin.
func NewAccessToken(secret []byte, adminID int) (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   fmt.Sprintf("%d", adminID),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenTTL)),
		Issuer:    "rumarasa-api",
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}

// ParseAccessToken validates signature, expiry, and issuer, returning the admin id.
func ParseAccessToken(secret []byte, tokenString string) (int, error) {
	token, err := jwt.ParseWithClaims(tokenString, &jwt.RegisteredClaims{},
		func(t *jwt.Token) (any, error) { return secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer("rumarasa-api"),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return 0, err
	}
	claims, ok := token.Claims.(*jwt.RegisteredClaims)
	if !ok {
		return 0, errors.New("unexpected claims type")
	}
	var adminID int
	if _, err := fmt.Sscanf(claims.Subject, "%d", &adminID); err != nil {
		return 0, fmt.Errorf("invalid subject claim: %w", err)
	}
	return adminID, nil
}

// NewRefreshToken returns an opaque 32-byte token and the sha256 hash to store.
func NewRefreshToken() (token, tokenHash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(raw)
	return token, HashToken(token), nil
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
