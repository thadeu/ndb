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
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"unicode"
)

// ErrOpen is a stored value that does not decrypt: another key, another
// field, a cut or changed value, or not a sealed value.
var ErrOpen = errors.New("the value does not decrypt")

// Norm is how a value is changed before its blind hash.
type Norm string

// The normalizations of a blind field.
const (
	Exact   Norm = "exact"
	Numeric Norm = "numeric"
	Lower   Norm = "lower"
	Upper   Norm = "upper"
)

const (
	sealPrefix = "ndb:e1:"
	hashPrefix = "ndb:h1:"
	hashLen    = 16
)

// Box holds the cipher and the hash key of the current master key and,
// during a key rotation, of the old one: new values use the current key,
// and a value of either key opens. The keys change with Rotate and DropOld,
// with no lock on the read side.
type Box struct {
	cur atomic.Pointer[keyset]
	old atomic.Pointer[keyset]
}

// keyset is the derived keys of one master key.
type keyset struct {
	id     string
	aead   cipher.AEAD
	hmac   []byte
	prefix string
}

func newKeyset(id string, encKey, hashKey []byte) (*keyset, error) {
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	return &keyset{id: id, aead: aead, hmac: hashKey, prefix: sealPrefix + id + ":"}, nil
}

// New makes a Box from the key id (k1) and the two derived keys.
func New(id string, encKey, hashKey []byte) (*Box, error) {
	ks, err := newKeyset(id, encKey, hashKey)
	if err != nil {
		return nil, err
	}

	b := &Box{}
	b.cur.Store(ks)

	return b, nil
}

// Rotate makes the key id the current key. The current key becomes the old
// one: its values still open until DropOld.
func (b *Box) Rotate(id string, encKey, hashKey []byte) error {
	ks, err := newKeyset(id, encKey, hashKey)
	if err != nil {
		return err
	}

	b.old.Store(b.cur.Load())
	b.cur.Store(ks)

	return nil
}

// SetOld gives the old key of a rotation that a restart cut.
func (b *Box) SetOld(id string, encKey, hashKey []byte) error {
	ks, err := newKeyset(id, encKey, hashKey)
	if err != nil {
		return err
	}

	b.old.Store(ks)

	return nil
}

// DropOld ends a rotation: the values of the old key no longer open.
func (b *Box) DropOld() {
	b.old.Store(nil)
}

// Rotating tells whether the box has an old key.
func (b *Box) Rotating() bool {
	return b.old.Load() != nil
}

// ID is the id of the current key (k1, k2...).
func (b *Box) ID() string {
	return b.cur.Load().id
}

// Prefix starts every value sealed with the current key: ndb:e1:k2:.
func (b *Box) Prefix() string {
	return b.cur.Load().prefix
}

// Seal encrypts a JSON value for a place (collection/field).
func (b *Box) Seal(place string, value json.RawMessage) (string, error) {
	ks := b.cur.Load()
	nonce := make([]byte, ks.aead.NonceSize(), ks.aead.NonceSize()+len(value)+ks.aead.Overhead())

	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	out := ks.aead.Seal(nonce, nonce, value, []byte(place))

	return ks.prefix + base64.RawURLEncoding.EncodeToString(out), nil
}

// Open decrypts a sealed value of a place back to its JSON, with the key
// that its prefix names: the current one or the old one.
func (b *Box) Open(place, sealed string) (json.RawMessage, error) {
	if plain, err := b.cur.Load().open(place, sealed); err == nil {
		return plain, nil
	}

	if old := b.old.Load(); old != nil {
		return old.open(place, sealed)
	}

	return nil, ErrOpen
}

func (ks *keyset) open(place, sealed string) (json.RawMessage, error) {
	rest, ok := strings.CutPrefix(sealed, ks.prefix)
	if !ok {
		return nil, ErrOpen
	}

	raw, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil || len(raw) < ks.aead.NonceSize() {
		return nil, ErrOpen
	}

	n := ks.aead.NonceSize()

	plain, err := ks.aead.Open(nil, raw[:n], raw[n:], []byte(place))
	if err != nil {
		return nil, ErrOpen
	}

	return plain, nil
}

// Hash is the blind hash of a JSON value for a place, with the current key.
// A string is normalized first; any other value is hashed as its JSON.
func (b *Box) Hash(place string, norm Norm, value json.RawMessage) (string, error) {
	return b.cur.Load().hash(place, norm, value), nil
}

// OldHash is the hash of the old key, during a rotation: the hash that the
// documents not written again still have.
func (b *Box) OldHash(place string, norm Norm, value json.RawMessage) (string, bool) {
	old := b.old.Load()
	if old == nil {
		return "", false
	}

	return old.hash(place, norm, value), true
}

func (ks *keyset) hash(place string, norm Norm, value json.RawMessage) string {
	data := []byte(value)

	var s string
	if json.Unmarshal(value, &s) == nil {
		data = []byte(normalize(norm, s))
	}

	mac := hmac.New(sha256.New, ks.hmac)
	mac.Write([]byte(place))
	mac.Write([]byte{0})
	mac.Write(data)

	return fmt.Sprintf("%s%s:%s", hashPrefix, ks.id, hex.EncodeToString(mac.Sum(nil)[:hashLen]))
}

// SealPrefix starts every sealed value.
const SealPrefix = sealPrefix

// IsSealed tells if a JSON value is a sealed string.
func IsSealed(value json.RawMessage) bool {
	return strings.HasPrefix(string(value), `"`+sealPrefix)
}

func normalize(norm Norm, s string) string {
	switch norm {
	case Numeric:
		return strings.Map(func(r rune) rune {
			if unicode.IsDigit(r) {
				return r
			}

			return -1
		}, s)
	case Lower:
		return strings.ToLower(strings.TrimSpace(s))
	case Upper:
		return strings.ToUpper(strings.TrimSpace(s))
	}

	return s
}

// PlainPattern is a regular expression that matches a plain string whose
// normalization is the one of s: what the hash of s finds.
func PlainPattern(norm Norm, s string) string {
	n := normalize(norm, s)

	switch norm {
	case Numeric:
		digits := make([]string, 0, len(n))

		for _, r := range n {
			digits = append(digits, regexp.QuoteMeta(string(r)))
		}

		return `^\D*` + strings.Join(digits, `\D*`) + `\D*$`
	case Lower, Upper:
		return `^\s*(?i:` + regexp.QuoteMeta(n) + `)\s*$`
	}

	return `^` + regexp.QuoteMeta(n) + `$`
}

// SealDoc seals the top-level fields of a JSON object that are in fields,
// for the collection coll. The order of the fields stays. A null stays
// null; a value already sealed for this place with the current key stays as
// it is, and one of the old key is sealed again with the current key.
func (b *Box) SealDoc(coll string, fields []string, doc json.RawMessage) (json.RawMessage, error) {
	cur := b.cur.Load()

	return rewrite(doc, fields, func(name string, v json.RawMessage) (json.RawMessage, error) {
		place := coll + "/" + name

		if string(v) == "null" || cur.opens(place, v) {
			return v, nil
		}

		if plain, ok := b.openOld(place, v); ok {
			v = plain
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
		s, ok := sealedText(v)
		if !ok {
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

// sealedText is the text of a sealed JSON string. A sealed value is ASCII
// with no escape, so the quotes are cut off with no decoding.
func sealedText(v json.RawMessage) (string, bool) {
	if !IsSealed(v) || len(v) < 2 || v[len(v)-1] != '"' || bytes.IndexByte(v, '\\') >= 0 {
		return "", false
	}

	return string(v[1 : len(v)-1]), true
}

func (ks *keyset) opens(place string, v json.RawMessage) bool {
	s, ok := sealedText(v)
	if !ok {
		return false
	}

	_, err := ks.open(place, s)

	return err == nil
}

// openOld opens a value of the old key.
func (b *Box) openOld(place string, v json.RawMessage) (json.RawMessage, bool) {
	old := b.old.Load()
	s, ok := sealedText(v)

	if old == nil || !ok {
		return nil, false
	}

	plain, err := old.open(place, s)

	return plain, err == nil
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

		if has && IsSealed(v) {
			plain, old := b.openOld(coll+"/"+f, v)
			if !old {
				continue // the hash of the current key is there
			}

			v = plain
		}

		switch {
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

var errNotObject = errors.New("crypt: the document is not a JSON object")

// members splits a JSON object into its top-level members, in order, in one
// pass over the bytes. The values are slices of doc.
func members(doc json.RawMessage) ([]member, error) {
	i := skipSpace(doc, 0)
	if i >= len(doc) || doc[i] != '{' {
		return nil, errNotObject
	}

	ms := make([]member, 0, 8)
	i = skipSpace(doc, i+1)

	if i < len(doc) && doc[i] == '}' {
		return ms, nil
	}

	for {
		if i >= len(doc) || doc[i] != '"' {
			return nil, errNotObject
		}

		end, err := scanString(doc, i)
		if err != nil {
			return nil, err
		}

		name, err := keyOf(doc[i:end])
		if err != nil {
			return nil, err
		}

		i = skipSpace(doc, end)
		if i >= len(doc) || doc[i] != ':' {
			return nil, errNotObject
		}

		i = skipSpace(doc, i+1)

		vend, err := scanValue(doc, i)
		if err != nil {
			return nil, err
		}

		ms = append(ms, member{name: name, val: doc[i:vend]})
		i = skipSpace(doc, vend)

		switch {
		case i < len(doc) && doc[i] == ',':
			i = skipSpace(doc, i+1)
		case i < len(doc) && doc[i] == '}':
			return ms, nil
		default:
			return nil, errNotObject
		}
	}
}

func keyOf(quoted []byte) (string, error) {
	if !bytes.ContainsRune(quoted, '\\') {
		return string(quoted[1 : len(quoted)-1]), nil
	}

	var name string

	return name, json.Unmarshal(quoted, &name)
}

func skipSpace(doc []byte, i int) int {
	for i < len(doc) && (doc[i] == ' ' || doc[i] == '\t' || doc[i] == '\n' || doc[i] == '\r') {
		i++
	}

	return i
}

// scanString gives the index after the closing quote of the string at i.
func scanString(doc []byte, i int) (int, error) {
	for j := i + 1; j < len(doc); j++ {
		switch doc[j] {
		case '\\':
			j++
		case '"':
			return j + 1, nil
		}
	}

	return 0, errNotObject
}

// scanValue gives the index after the JSON value at i.
func scanValue(doc []byte, i int) (int, error) {
	if i >= len(doc) {
		return 0, errNotObject
	}

	switch doc[i] {
	case '"':
		return scanString(doc, i)
	case '{', '[':
		return scanNested(doc, i)
	}

	j := i
	for j < len(doc) && !strings.ContainsRune(",}] \t\n\r", rune(doc[j])) {
		j++
	}

	if j == i {
		return 0, errNotObject
	}

	return j, nil
}

func scanNested(doc []byte, i int) (int, error) {
	depth := 0

	for j := i; j < len(doc); j++ {
		switch doc[j] {
		case '"':
			end, err := scanString(doc, j)
			if err != nil {
				return 0, err
			}

			j = end - 1
		case '{', '[':
			depth++
		case '}', ']':
			depth--

			if depth == 0 {
				return j + 1, nil
			}
		}
	}

	return 0, errNotObject
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
