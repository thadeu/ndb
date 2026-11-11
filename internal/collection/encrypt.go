package collection

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/crypt"
)

// encState is the encrypted and blind fields of the collection.
type encState struct {
	fields []string
	blind  map[string]crypt.Norm
}

// SetEncrypted takes the encrypted and blind fields of def. A field is
// sealed from the next write on; the stored documents stay as they are.
func (c *Collection) SetEncrypted(def catalog.Def) {
	st := encState{fields: slices.Clone(def.Encrypted), blind: map[string]crypt.Norm{}}

	for f, norm := range def.Blind {
		st.blind[f] = crypt.Norm(norm)
	}

	c.enc.Store(&st)
}

func (c *Collection) encState() encState {
	if st := c.enc.Load(); st != nil {
		return *st
	}

	return encState{}
}

// Encrypted is the list of encrypted fields.
func (c *Collection) Encrypted() []string {
	return c.encState().fields
}

// encrypt sets the blind hashes and seals the encrypted fields of a
// document, before the WAL.
func (c *Collection) encrypt(doc json.RawMessage) (json.RawMessage, error) {
	st := c.encState()
	if len(st.fields) == 0 {
		return doc, nil
	}

	if c.box == nil {
		return nil, errf("internal", "the collection has encrypted fields and no key")
	}

	doc, err := c.box.HashDoc(c.def.Place(), st.blind, doc)
	if err != nil {
		return nil, err
	}

	return c.box.SealDoc(c.def.Place(), st.fields, doc)
}

// splitOps is splitBody with the rule of encrypted fields: only $set,
// $setOnInsert and $unset may change one. A sum or a push needs the value.
func (c *Collection) splitOps(top map[string]any) (map[string]any, []fieldOp, error) {
	filter, ops, err := splitBody(top)
	if err != nil {
		return nil, nil, err
	}

	fields := c.Encrypted()

	for _, o := range ops {
		top, _, _ := strings.Cut(o.path, ".")

		if !slices.Contains(fields, top) {
			continue
		}

		if o.op != "$set" && o.op != "$setOnInsert" && o.op != "$unset" || top != o.path {
			return nil, nil, errf("invalid_query", "%s is encrypted: only $set and $unset of the whole field change it", top)
		}
	}

	return filter, ops, nil
}

// filterField is the query hook of encrypted fields. $exists works on any
// of them. On a blind field, $eq, $in, $ne and $nin compare the hash of the
// value with <field>_bidx. Any other condition needs the value: refused.
func (c *Collection) filterField(path string, v any) (string, any, error) {
	st := c.encState()
	if !slices.Contains(st.fields, path) {
		return path, v, nil
	}

	m, isOps := v.(map[string]any)
	if isOps && len(m) == 1 && m["$exists"] != nil {
		return path, v, nil
	}

	norm, blind := st.blind[path]
	if !blind {
		return "", nil, errf("invalid_query", "%s is encrypted: only $exists filters it (declare it in blind for $eq and $in)", path)
	}

	hash := func(x any) (any, error) {
		raw, err := json.Marshal(x)
		if err != nil {
			return nil, err
		}

		return c.box.Hash(c.def.Place()+"/"+path, norm, raw)
	}

	if !isOps || !strings.HasPrefix(firstKey(m), "$") {
		h, err := hash(v)

		return path + crypt.HashSuffix, h, err
	}

	out := make(map[string]any, len(m))

	for op, x := range m {
		var err error

		switch op {
		case "$eq", "$ne":
			out[op], err = hash(x)
		case "$in", "$nin":
			out[op], err = hashList(x, hash)
		case "$exists":
			out[op] = x
		default:
			err = errf("invalid_query", "%s is encrypted: %s needs the value (only $eq, $in, $ne, $nin, $exists)", path, op)
		}

		if err != nil {
			return "", nil, err
		}
	}

	return path + crypt.HashSuffix, out, nil
}

// checkAggRefs refuses a stage that reads an encrypted field ("$cpf"): a
// group, a sum or a projection of it would work on the sealed value. A
// group by a blind field names its hash: "$cpf_bidx".
func (c *Collection) checkAggRefs(body []byte) error {
	for _, f := range c.Encrypted() {
		if bytes.Contains(body, []byte(`"$`+f+`"`)) || bytes.Contains(body, []byte(`"$`+f+`.`)) {
			return errf("invalid_query", "%s is encrypted: a stage cannot read it (group by \"$%s%s\" when it is blind)", f, f, crypt.HashSuffix)
		}
	}

	return nil
}

func hashList(x any, hash func(any) (any, error)) (any, error) {
	list, ok := x.([]any)
	if !ok {
		return x, nil
	}

	out := make([]any, len(list))

	for i, e := range list {
		h, err := hash(e)
		if err != nil {
			return nil, err
		}

		out[i] = h
	}

	return out, nil
}

func firstKey(m map[string]any) string {
	for k := range m {
		return k
	}

	return ""
}
