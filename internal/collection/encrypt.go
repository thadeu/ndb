package collection

import (
	"encoding/json"
	"slices"
	"strings"
)

// SetEncrypted sets the encrypted fields of the collection. A field is
// sealed from the next write on; the stored documents stay as they are.
func (c *Collection) SetEncrypted(fields []string) {
	f := slices.Clone(fields)

	c.encrypted.Store(&f)
}

// Encrypted is the list of encrypted fields.
func (c *Collection) Encrypted() []string {
	if f := c.encrypted.Load(); f != nil {
		return *f
	}

	return nil
}

// encrypt seals the encrypted fields of a document before the WAL.
func (c *Collection) encrypt(doc json.RawMessage) (json.RawMessage, error) {
	fields := c.Encrypted()
	if len(fields) == 0 {
		return doc, nil
	}

	if c.box == nil {
		return nil, errf("internal", "the collection has encrypted fields and no key")
	}

	return c.box.SealDoc(c.def.Place(), fields, doc)
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
