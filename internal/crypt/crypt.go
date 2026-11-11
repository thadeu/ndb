// Package crypt encrypts the values of encrypted fields and makes the hashes
// of blind indexes. A Box is made once, at open, and is safe for concurrent
// use: a call does no key derivation and takes no lock.
package crypt

import (
	"bytes"
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
	"slices"
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

// SealDoc seals the top-level fields of a JSON object that are in fields,
// for the collection coll. The order of the fields stays. A null stays
// null; a value already sealed for this place stays as it is.
func (b *Box) SealDoc(coll string, fields []string, doc json.RawMessage) (json.RawMessage, error) {
	return rewrite(doc, fields, func(name string, v json.RawMessage) (json.RawMessage, error) {
		place := coll + "/" + name

		if string(v) == "null" || b.opens(place, v) {
			return v, nil
		}

		s, err := b.Seal(place, v)
		if err != nil {
			return nil, err
		}

		return json.Marshal(s)
	})
}

// OpenDoc opens the sealed top-level fields of a JSON object. A value that
// does not open stays sealed.
func (b *Box) OpenDoc(coll string, fields []string, doc json.RawMessage) json.RawMessage {
	out, err := rewrite(doc, fields, func(name string, v json.RawMessage) (json.RawMessage, error) {
		var s string

		if !IsSealed(v) || json.Unmarshal(v, &s) != nil {
			return v, nil
		}

		if plain, err := b.Open(coll+"/"+name, s); err == nil {
			return plain, nil
		}

		return v, nil
	})
	if err != nil {
		return doc
	}

	return out
}

func (b *Box) opens(place string, v json.RawMessage) bool {
	var s string

	if !IsSealed(v) || json.Unmarshal(v, &s) != nil {
		return false
	}

	_, err := b.Open(place, s)

	return err == nil
}

// HashDoc sets <field>_bidx, the blind hash of each field of blind, from
// its plain value. A sealed value keeps the hash it has; a missing or null
// field has no hash. A hash field sent by the caller is never kept as is.
func (b *Box) HashDoc(coll string, blind map[string]Norm, doc json.RawMessage) (json.RawMessage, error) {
	if len(blind) == 0 {
		return doc, nil
	}

	ms, err := members(doc)
	if err != nil {
		return nil, err
	}

	for f, norm := range blind {
		hashKey := f + HashSuffix
		v, has := get(ms, f)

		switch {
		case has && IsSealed(v):
			continue
		case !has || string(v) == "null":
			ms = del(ms, hashKey)
		default:
			h, err := b.Hash(coll+"/"+f, norm, v)
			if err != nil {
				return nil, err
			}

			hv, _ := json.Marshal(h)
			ms = set(ms, hashKey, hv)
		}
	}

	return encode(ms), nil
}

// HashSuffix ends the name of the hash field of a blind field.
const HashSuffix = "_bidx"

type member struct {
	name string
	val  json.RawMessage
}

// rewrite calls fn on each top-level member of doc named in fields and
// rebuilds the object in its order. A doc with none of them is returned
// as it is.
func rewrite(doc json.RawMessage, fields []string, fn func(string, json.RawMessage) (json.RawMessage, error)) (json.RawMessage, error) {
	if len(fields) == 0 || !mentions(doc, fields) {
		return doc, nil
	}

	ms, err := members(doc)
	if err != nil {
		return nil, err
	}

	for i, m := range ms {
		if !slices.Contains(fields, m.name) {
			continue
		}

		if ms[i].val, err = fn(m.name, m.val); err != nil {
			return nil, err
		}
	}

	return encode(ms), nil
}

// DropFields removes top-level fields of a JSON object.
func DropFields(doc json.RawMessage, names []string) json.RawMessage {
	if !mentions(doc, names) {
		return doc
	}

	ms, err := members(doc)
	if err != nil {
		return doc
	}

	for _, n := range names {
		ms = del(ms, n)
	}

	return encode(ms)
}

func members(doc json.RawMessage) ([]member, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()

	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("crypt: the document is not an object")
	}

	var ms []member

	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}

		name, _ := tok.(string)

		var v json.RawMessage

		if err := dec.Decode(&v); err != nil {
			return nil, err
		}

		ms = append(ms, member{name: name, val: v})
	}

	return ms, nil
}

func encode(ms []member) json.RawMessage {
	out := []byte{'{'}

	for i, m := range ms {
		if i > 0 {
			out = append(out, ',')
		}

		key, _ := json.Marshal(m.name)

		out = append(out, key...)
		out = append(out, ':')
		out = append(out, m.val...)
	}

	return append(out, '}')
}

func get(ms []member, name string) (json.RawMessage, bool) {
	for _, m := range ms {
		if m.name == name {
			return m.val, true
		}
	}

	return nil, false
}

func set(ms []member, name string, v json.RawMessage) []member {
	for i, m := range ms {
		if m.name == name {
			ms[i].val = v

			return ms
		}
	}

	return append(ms, member{name: name, val: v})
}

func del(ms []member, name string) []member {
	return slices.DeleteFunc(ms, func(m member) bool { return m.name == name })
}

func mentions(doc json.RawMessage, fields []string) bool {
	for _, f := range fields {
		if bytes.Contains(doc, []byte(`"`+f+`"`)) {
			return true
		}
	}

	return false
}
