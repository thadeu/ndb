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
	"strconv"
	"strings"
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
//
// A key rotation (ndb key rotate) makes key k<n+1> the current key. The old
// key stays, only to read, until every encrypted value is sealed again with
// the new one; then Finish drops it. A stored key rotates in place. An env
// key rotates at the next start: PlanEnv keeps the fingerprint of the new
// key, and the start takes it in NDB_ENCRYPTION_KEY with the old one in
// NDB_ENCRYPTION_KEY_OLD (or in the settings, for a stored key that moves
// to the env).

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

	// Old is the key of a rotation that is not finished: its values still
	// open. Nil when there is none.
	Old *Key

	master []byte
}

type storedKey struct {
	ID          string    `json:"id"`
	Key         string    `json:"key,omitempty"`
	Fingerprint string    `json:"fingerprint"`
	Source      string    `json:"source"`
	Created     time.Time `json:"created_at"`

	// Previous is the old key of a rotation that is not finished (its
	// Key is empty for an env key). Next is the fingerprint of the env key
	// that PlanEnv made, for the next start.
	Previous *storedKey `json:"previous,omitempty"`
	Next     string     `json:"next,omitempty"`
}

// LoadKey gives the key of the daemon from env (the value of
// NDB_ENCRYPTION_KEY, "" for none) and the settings s.
func LoadKey(s *Store, env string) (Key, error) {
	return LoadKeys(s, env, "")
}

// LoadKeys is LoadKey with oldEnv, the value of NDB_ENCRYPTION_KEY_OLD: the
// old key of an env rotation.
func LoadKeys(s *Store, env, oldEnv string) (Key, error) {
	stored, err := loadStored(s)
	if err != nil {
		return Key{}, err
	}

	if env != "" {
		return envKey(s, env, oldEnv, stored)
	}

	switch {
	case stored == nil:
		return makeKey(s)
	case stored.Key == "":
		return Key{}, errors.New("the encryption key of this data dir comes from NDB_ENCRYPTION_KEY, and it is not set")
	}

	k, err := stored.key("stored")
	if err != nil {
		return Key{}, err
	}

	if stored.Previous != nil {
		old, err := stored.Previous.key("stored")
		if err != nil {
			return Key{}, err
		}

		k.Old = &old
	}

	return k, nil
}

func loadStored(s *Store) (*storedKey, error) {
	raw, ok := s.Get(KeySetting)
	if !ok {
		return nil, nil
	}

	stored := &storedKey{}

	if err := json.Unmarshal(raw, stored); err != nil {
		return nil, fmt.Errorf("settings: %s: %w", KeySetting, err)
	}

	return stored, nil
}

func (sk *storedKey) key(source string) (Key, error) {
	master, err := hex.DecodeString(sk.Key)
	if err != nil || len(master) != 32 {
		return Key{}, fmt.Errorf("settings: %s is not 32 bytes of hex", KeySetting)
	}

	return Key{ID: sk.ID, Source: source, Created: sk.Created, master: master}, nil
}

func envKey(s *Store, env, oldEnv string, stored *storedKey) (Key, error) {
	k, err := hexKey("NDB_ENCRYPTION_KEY", env)
	if err != nil {
		return Key{}, err
	}

	k.ID, k.Source, k.Created = "k1", "env", time.Now().UTC()

	switch {
	case stored == nil:
		return k, s.Set(map[string]json.RawMessage{KeySetting: mustJSON(storedKey{ID: k.ID, Fingerprint: k.Fingerprint(), Source: "env", Created: k.Created})})
	case same(stored.Fingerprint, k.Fingerprint()):
		k.ID, k.Created = stored.ID, stored.Created

		if stored.Previous != nil {
			old, err := oldKey(stored.Previous, oldEnv)
			if err != nil {
				return Key{}, fmt.Errorf("the rotation to %s is not finished: %w", k.ID, err)
			}

			k.Old = &old
		}

		return k, nil
	case stored.Next != "" && same(stored.Next, k.Fingerprint()):
		return startEnvRotation(s, k, oldEnv, stored)
	}

	return Key{}, errors.New("NDB_ENCRYPTION_KEY is not the key of this data dir: the encrypted values would not be readable")
}

// startEnvRotation is the first start with the env key that PlanEnv made.
func startEnvRotation(s *Store, k Key, oldEnv string, stored *storedKey) (Key, error) {
	if stored.Previous != nil {
		return Key{}, errors.New("a key rotation is not finished: start with the current key and NDB_ENCRYPTION_KEY_OLD until it ends")
	}

	old, err := oldKey(stored, oldEnv)
	if err != nil {
		return Key{}, fmt.Errorf("NDB_ENCRYPTION_KEY is the new key of a rotation: %w", err)
	}

	k.ID, k.Old = nextID(stored.ID), &old

	prev := *stored
	prev.Next, prev.Previous = "", nil

	if prev.Source == "env" {
		prev.Key = ""
	}

	next := storedKey{ID: k.ID, Fingerprint: k.Fingerprint(), Source: "env", Created: k.Created, Previous: &prev}

	return k, s.Set(map[string]json.RawMessage{KeySetting: mustJSON(next)})
}

// oldKey is the key of sk: from the settings, or from NDB_ENCRYPTION_KEY_OLD
// for an env key.
func oldKey(sk *storedKey, oldEnv string) (Key, error) {
	if sk.Key != "" {
		return sk.key("stored")
	}

	if oldEnv == "" {
		return Key{}, fmt.Errorf("set NDB_ENCRYPTION_KEY_OLD to the key %s", sk.ID)
	}

	old, err := hexKey("NDB_ENCRYPTION_KEY_OLD", oldEnv)
	if err != nil {
		return Key{}, err
	}

	if !same(sk.Fingerprint, old.Fingerprint()) {
		return Key{}, fmt.Errorf("NDB_ENCRYPTION_KEY_OLD is not the key %s of this data dir", sk.ID)
	}

	old.ID, old.Source, old.Created = sk.ID, "env", sk.Created

	return old, nil
}

func hexKey(name, value string) (Key, error) {
	master, err := hex.DecodeString(value)
	if err != nil || len(master) != 32 {
		return Key{}, fmt.Errorf("%s must be 64 hex characters (32 bytes)", name)
	}

	return Key{master: master}, nil
}

func same(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// NextID is the id after id: k1 gives k2.
func NextID(id string) string {
	return nextID(id)
}

func nextID(id string) string {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "k"))
	if err != nil {
		n = 1
	}

	return "k" + strconv.Itoa(n+1)
}

// ErrRotating is a rotation asked while another one runs.
var ErrRotating = errors.New("a key rotation is not finished yet")

// Rotate makes a new stored key the current one; cur becomes the old key.
// An env key rotates with PlanEnv.
func Rotate(s *Store, cur Key) (Key, error) {
	stored, err := loadStored(s)
	if err != nil {
		return Key{}, err
	}

	switch {
	case cur.Old != nil || stored != nil && stored.Previous != nil:
		return Key{}, ErrRotating
	case stored == nil || stored.Key == "":
		return Key{}, errors.New("the key comes from NDB_ENCRYPTION_KEY: rotate it with ndb key rotate -env")
	}

	master := make([]byte, 32)

	if _, err := rand.Read(master); err != nil {
		return Key{}, err
	}

	k := Key{ID: nextID(stored.ID), Source: "stored", Created: time.Now().UTC(), master: master}
	old := cur
	k.Old = &old

	prev := *stored
	prev.Next = ""

	next := storedKey{ID: k.ID, Key: hex.EncodeToString(master), Fingerprint: k.Fingerprint(), Source: "made", Created: k.Created, Previous: &prev}

	return k, s.Set(map[string]json.RawMessage{KeySetting: mustJSON(next)})
}

// PlanEnv makes the env key of the next rotation and keeps its
// fingerprint. It gives the key in hex: the caller shows it once.
func PlanEnv(s *Store, cur Key) (string, error) {
	stored, err := loadStored(s)
	if err != nil {
		return "", err
	}

	if cur.Old != nil || stored == nil || stored.Previous != nil {
		return "", ErrRotating
	}

	master := make([]byte, 32)

	if _, err := rand.Read(master); err != nil {
		return "", err
	}

	next := *stored
	next.Next = Key{master: master}.Fingerprint()

	if err := s.Set(map[string]json.RawMessage{KeySetting: mustJSON(next)}); err != nil {
		return "", err
	}

	return hex.EncodeToString(master), nil
}

// Finish ends a rotation: the settings drop the old key, and the file is
// written again with no copy of it.
func Finish(s *Store) error {
	stored, err := loadStored(s)
	if err != nil || stored == nil || stored.Previous == nil {
		return err
	}

	stored.Previous = nil

	if err := s.Set(map[string]json.RawMessage{KeySetting: mustJSON(stored)}); err != nil {
		return err
	}

	return s.Compact()
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
