// Package ingestkey generates and hashes per-app ingest keys (D7).
//
// A key is "sk_" followed by 32 random bytes in unpadded base64url (256 bits
// of entropy). Only its SHA-256 is stored: with that much entropy a plain,
// unsalted hash cannot be brute-forced, and it stays a deterministic value we
// can look up through a unique index on every ingest request. A slow KDF
// (bcrypt, argon2) would add nothing but per-request CPU cost.
package ingestkey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// Prefix marks Sonda ingest keys, so they are recognisable in configs and logs.
const Prefix = "sk_"

// Len is the length of a key: Prefix + 32 bytes in unpadded base64url.
const Len = len(Prefix) + 43

// Generate returns a new random key and its hash.
func Generate() (key, hash string) {
	b := make([]byte, 32)
	rand.Read(b) // crypto/rand.Read never returns an error (Go ≥ 1.24)
	key = Prefix + base64.RawURLEncoding.EncodeToString(b)
	return key, Hash(key)
}

// Hash returns the hex SHA-256 of key, the form stored in apps.ingest_key_hash.
func Hash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// WellFormed reports whether key has the shape of a generated key: Prefix
// followed by exactly 43 base64url characters. It lets the server refuse junk
// before touching the database (O6 M1); it says nothing about whether the
// key belongs to an app.
func WellFormed(key string) bool {
	if len(key) != Len || key[:len(Prefix)] != Prefix {
		return false
	}
	for i := len(Prefix); i < len(key); i++ {
		c := key[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
