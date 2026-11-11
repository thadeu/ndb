package settings

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// The encryption key of the daemon: one master key of 32 bytes, that
// encrypts the encrypted fields and makes the blind indexes (M28) through
// keys derived from it. At start the daemon takes, in this order:
//
//  1. the env NDB_ENCRYPTION_KEY (64 hex characters). It is not written to
//     the settings; only its fingerprint is, so a later start with no env
//     or with another key is an error, not a new key that reads nothing;
//  2. the key in the settings;
//  3. none: it makes one and writes it to the settings.

// KeySetting is the settings key of the encryption key.
const KeySetting = "encryption.key"

// Key is the encryption key of the daemon.
type Key struct {
	// ID names the key in a stored value (ndb:e1:k1:...). It is k1 until
	// keys rotate.
	ID string

	// Source is where the key came from: env, stored or made.
	Source string

	Created time.Time

	master []byte
}

type storedKey struct {
	ID          string    `json:"id"`
	Key         string    `json:"key,omitempty"`
	Fingerprint string    `json:"fingerprint"`
	Source      string    `json:"source"`
	Created     time.Time `json:"created_at"`
}

// LoadKey gives the key of the daemon from env (the value of
// NDB_ENCRYPTION_KEY, "" for none) and the settings s.
func LoadKey(s *Store, env string) (Key, error) {
	var stored *storedKey

	if raw, ok := s.Get(KeySetting); ok {
		stored = &storedKey{}

		if err := json.Unmarshal(raw, stored); err != nil {
			return Key{}, fmt.Errorf("settings: %s: %w", KeySetting, err)
		}
	}

	if env != "" {
		return envKey(s, env, stored)
	}

	switch {
	case stored == nil:
		return makeKey(s)
	case stored.Key == "":
		return Key{}, errors.New("the encryption key of this data dir comes from NDB_ENCRYPTION_KEY, and it is not set")
	}

	master, err := hex.DecodeString(stored.Key)
	if err != nil || len(master) != 32 {
		return Key{}, fmt.Errorf("settings: %s is not 32 bytes of hex", KeySetting)
	}

	return Key{ID: stored.ID, Source: "stored", Created: stored.Created, master: master}, nil
}

func envKey(s *Store, env string, stored *storedKey) (Key, error) {
	master, err := hex.DecodeString(env)
	if err != nil || len(master) != 32 {
		return Key{}, errors.New("NDB_ENCRYPTION_KEY must be 64 hex characters (32 bytes)")
	}

	k := Key{ID: "k1", Source: "env", Created: time.Now().UTC(), master: master}

	if stored == nil {
		return k, s.Set(map[string]json.RawMessage{KeySetting: mustJSON(storedKey{ID: k.ID, Fingerprint: k.Fingerprint(), Source: "env", Created: k.Created})})
	}

	if subtle.ConstantTimeCompare([]byte(stored.Fingerprint), []byte(k.Fingerprint())) != 1 {
		return Key{}, errors.New("NDB_ENCRYPTION_KEY is not the key of this data dir: the encrypted values would not be readable")
	}

	k.ID, k.Created = stored.ID, stored.Created

	return k, nil
}

func makeKey(s *Store) (Key, error) {
	master := make([]byte, 32)

	if _, err := rand.Read(master); err != nil {
		return Key{}, err
	}

	k := Key{ID: "k1", Source: "made", Created: time.Now().UTC(), master: master}

	err := s.Set(map[string]json.RawMessage{KeySetting: mustJSON(storedKey{
		ID: k.ID, Key: hex.EncodeToString(master), Fingerprint: k.Fingerprint(), Source: "made", Created: k.Created,
	})})

	return k, err
}

// Master is the master key. Only the code that derives keys reads it.
func (k Key) Master() []byte {
	return k.master
}

// Masked is the key with only its first and last 4 hex characters, for an
// answer of the API.
func (k Key) Masked() string {
	h := hex.EncodeToString(k.master)

	return h[:4] + "****…****" + h[len(h)-4:]
}

// Fingerprint names the key with no part of it: the first 8 bytes of its
// SHA-256, in hex.
func (k Key) Fingerprint() string {
	sum := sha256.Sum256(k.master)

	return hex.EncodeToString(sum[:8])
}

// Derive gives the key of one purpose ("ndb:e1" to encrypt, "ndb:h1" for
// the blind indexes), so the master key is never used as it is.
func (k Key) Derive(purpose string) []byte {
	out, err := hkdf.Key(sha256.New, k.master, nil, purpose, 32)
	if err != nil {
		panic(err)
	}

	return out
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}

	return b
}
