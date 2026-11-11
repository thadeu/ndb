// Package crypt encrypts the values of encrypted fields and makes the hashes
// of blind indexes. A Box is made once, at open, and is safe for concurrent
// use: a call does no key derivation and takes no lock.
package crypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// ErrOpen is a stored value that does not decrypt: another key, another
// field, a cut or changed value, or not a sealed value.
var ErrOpen = errors.New("the value does not decrypt")

// Norm is how a value is changed before its blind hash.
type Norm string

// The normalizations of a blind field.
const (
	Exact  Norm = "exact"
	Digits Norm = "digits"
	Lower  Norm = "lower"
)

const (
	sealPrefix = "ndb:e1:"
	hashPrefix = "ndb:h1:"
	hashLen    = 16
)

// Box holds the cipher and the hash key of one master key.
type Box struct {
	id     string
	aead   cipher.AEAD
	hmac   []byte
	prefix string
}

// New makes a Box from the key id (k1) and the two derived keys.
func New(id string, encKey, hashKey []byte) (*Box, error) {
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	return &Box{id: id, aead: aead, hmac: hashKey, prefix: sealPrefix + id + ":"}, nil
}

// Seal encrypts a JSON value for a place (collection/field).
func (b *Box) Seal(place string, value json.RawMessage) (string, error) {
	nonce := make([]byte, b.aead.NonceSize(), b.aead.NonceSize()+len(value)+b.aead.Overhead())

	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	out := b.aead.Seal(nonce, nonce, value, []byte(place))

	return b.prefix + base64.RawURLEncoding.EncodeToString(out), nil
}

// Open decrypts a sealed value of a place back to its JSON.
func (b *Box) Open(place, sealed string) (json.RawMessage, error) {
	rest, ok := strings.CutPrefix(sealed, b.prefix)
	if !ok {
		return nil, ErrOpen
	}

	raw, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil || len(raw) < b.aead.NonceSize() {
		return nil, ErrOpen
	}

	n := b.aead.NonceSize()

	plain, err := b.aead.Open(nil, raw[:n], raw[n:], []byte(place))
	if err != nil {
		return nil, ErrOpen
	}

	return plain, nil
}

// Hash is the blind hash of a JSON value for a place. A string is
// normalized first; any other value is hashed as its JSON.
func (b *Box) Hash(place string, norm Norm, value json.RawMessage) (string, error) {
	data := []byte(value)

	var s string
	if json.Unmarshal(value, &s) == nil {
		data = []byte(normalize(norm, s))
	}

	mac := hmac.New(sha256.New, b.hmac)
	mac.Write([]byte(place))
	mac.Write([]byte{0})
	mac.Write(data)

	return fmt.Sprintf("%s%s:%s", hashPrefix, b.id, hex.EncodeToString(mac.Sum(nil)[:hashLen])), nil
}

// IsSealed tells if a JSON value is a sealed string.
func IsSealed(value json.RawMessage) bool {
	return strings.HasPrefix(string(value), `"`+sealPrefix)
}

func normalize(norm Norm, s string) string {
	switch norm {
	case Digits:
		return strings.Map(func(r rune) rune {
			if unicode.IsDigit(r) {
				return r
			}

			return -1
		}, s)
	case Lower:
		return strings.ToLower(strings.TrimSpace(s))
	}

	return s
}
