package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/crypt"
	"github.com/thadeu/ndb/internal/engine"
)

// groupHelper is the prefix of the hidden accumulator that keeps one sealed
// value of a blind field in each group.
const groupHelper = "_ndb_group_"

// groupKey is a blind field in the _id of a $group: the whole _id (path
// "") or one key of it.
type groupKey struct {
	field, path string
}

// withBlindGroups runs an aggregate whose $group is by a blind field: the
// group is by its hash, and the _id of each group gets the value back,
// plain for a caller that may decrypt, else sealed.
func (s *Server) withBlindGroups(r *http.Request, db *engine.DB, run func(context.Context, []byte) (collection.Page, error)) func(context.Context, []byte) (collection.Page, error) {
	return func(ctx context.Context, body []byte) (collection.Page, error) {
		coll := r.PathValue("coll")

		def, ok := db.Definition(coll)
		if !ok || len(def.Blind) == 0 {
			return run(ctx, body)
		}

		rewritten, keys, err := groupByHash(body, def.Blind)
		if err != nil || len(keys) == 0 {
			return run(ctx, body)
		}

		page, err := run(ctx, rewritten)
		if err != nil {
			return page, err
		}

		plain := mayDecrypt(r, coll)

		for i, row := range page.Rows {
			page.Rows[i] = restoreGroup(row, keys, func(field string, sealed json.RawMessage) json.RawMessage {
				if !plain {
					return sealed
				}

				var text string

				if json.Unmarshal(sealed, &text) != nil {
					return sealed
				}

				if v, err := db.Box().Open(def.Place()+"/"+field, text); err == nil {
					return v
				}

				return sealed
			})
		}

		return page, nil
	}
}

// groupByHash changes "$cpf" in the _id of each $group to "$cpf_bidx" and
// adds an accumulator that keeps the sealed value.
func groupByHash(body []byte, blind map[string]string) ([]byte, map[string]groupKey, error) {
	var top map[string]json.RawMessage

	if err := json.Unmarshal(body, &top); err != nil {
		return nil, nil, err
	}

	var stages []map[string]json.RawMessage

	if err := json.Unmarshal(top["pipeline"], &stages); err != nil {
		return nil, nil, err
	}

	keys := map[string]groupKey{}

	for _, st := range stages {
		raw, ok := st["$group"]
		if !ok {
			continue
		}

		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()

		var group map[string]any

		if err := dec.Decode(&group); err != nil {
			return nil, nil, err
		}

		ref := func(v any, path string) any {
			field, isRef := blindRef(v, blind)
			if !isRef {
				return v
			}

			helper := groupHelper + field + "_" + path
			group[helper] = map[string]any{"$first": "$" + field}
			keys[helper] = groupKey{field: field, path: path}

			return "$" + field + crypt.HashSuffix
		}

		if id, isObj := group["_id"].(map[string]any); isObj {
			for k, v := range id {
				id[k] = ref(v, k)
			}
		} else {
			group["_id"] = ref(group["_id"], "")
		}

		st["$group"], _ = json.Marshal(group)
	}

	if len(keys) == 0 {
		return body, nil, nil
	}

	top["pipeline"], _ = json.Marshal(stages)

	out, err := json.Marshal(top)

	return out, keys, err
}

func blindRef(v any, blind map[string]string) (string, bool) {
	s, ok := v.(string)
	if !ok || !strings.HasPrefix(s, "$") {
		return "", false
	}

	_, isBlind := blind[s[1:]]

	return s[1:], isBlind
}

// restoreGroup puts the value of each blind key back in the _id of a row and
// drops the helpers. A row with no helper (a later stage dropped it) stays.
func restoreGroup(row json.RawMessage, keys map[string]groupKey, value func(string, json.RawMessage) json.RawMessage) json.RawMessage {
	if !bytes.Contains(row, []byte(groupHelper)) {
		return row
	}

	var m map[string]json.RawMessage

	if json.Unmarshal(row, &m) != nil {
		return row
	}

	var id map[string]json.RawMessage

	_ = json.Unmarshal(m["_id"], &id)

	for helper, k := range keys {
		sealed, ok := m[helper]
		if !ok {
			continue
		}

		delete(m, helper)

		if k.path == "" {
			m["_id"] = value(k.field, sealed)
		} else if id != nil {
			id[k.path] = value(k.field, sealed)
		}
	}

	if id != nil {
		m["_id"], _ = json.Marshal(id)
	}

	out, err := json.Marshal(m)
	if err != nil {
		return row
	}

	return out
}
