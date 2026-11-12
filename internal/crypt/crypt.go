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

func (b *Box) opens(place string, v json.RawMessage) bool {
	s, ok := sealedText(v)
	if !ok {
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
