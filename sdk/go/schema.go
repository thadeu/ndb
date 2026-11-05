package ndb

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// schema is the mapping from a Go struct to a document, read from the
// `ndb:"name[,option...]"` tags. Options: id, time, index, unique, and
// version (the field holds "_v"). A field without an ndb tag uses its json
// name or its Go name; `ndb:"-"` or `json:"-"` skips it.
type schema struct {
	fields  []schemaField
	id      string
	time    string
	indexes []string
	unique  []string

	// version is the field that holds "_v". It is filled on reads and
	// never sent on writes.
	version string
}

type schemaField struct {
	name  string
	index int
}

func schemaOf(t reflect.Type) (*schema, error) {
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("ndb: %v is not a struct", t)
	}

	s := &schema{}

	for i := range t.NumField() {
		sf := t.Field(i)

		tag, ok := sf.Tag.Lookup("ndb")

		if tag == "-" {
			continue
		}

		if !ok {
			s.addUntagged(sf, i)

			continue
		}

		if err := s.add(sf, i, tag); err != nil {
			return nil, err
		}
	}

	return s, nil
}

// addUntagged stores an exported field without an ndb tag under its json
// tag name, or its Go name. json:"-" skips it.
func (s *schema) addUntagged(sf reflect.StructField, i int) {
	if !sf.IsExported() {
		return
	}

	name := sf.Name

	if jt, ok := sf.Tag.Lookup("json"); ok {
		jn, _, _ := strings.Cut(jt, ",")

		if jn == "-" {
			return
		}

		if jn != "" {
			name = jn
		}
	}

	s.fields = append(s.fields, schemaField{name: name, index: i})
}

func (s *schema) add(sf reflect.StructField, i int, tag string) error {
	parts := strings.Split(tag, ",")
	name := parts[0]

	if name == "" {
		return fmt.Errorf("ndb: field %s has no name in its tag", sf.Name)
	}

	if !sf.IsExported() {
		return fmt.Errorf("ndb: field %s is tagged but not exported", sf.Name)
	}

	s.fields = append(s.fields, schemaField{name: name, index: i})

	for _, opt := range parts[1:] {
		if err := s.option(name, opt); err != nil {
			return err
		}
	}

	return nil
}

func (s *schema) option(name, opt string) error {
	switch opt {
	case "id":
		if s.id != "" {
			return fmt.Errorf("ndb: more than one id field (%s and %s)", s.id, name)
		}

		s.id = name
	case "time":
		if s.time != "" {
			return fmt.Errorf("ndb: more than one time field (%s and %s)", s.time, name)
		}

		s.time = name
	case "index":
		s.indexes = append(s.indexes, name)
	case "unique":
		s.unique = append(s.unique, name)
	case "version":
		s.version = name
	default:
		return fmt.Errorf("ndb: field %s has unknown option %q", name, opt)
	}

	return nil
}

// idField is the document field of the id. The daemon default is _id.
func (s *schema) idField() string {
	if s.id == "" {
		return "_id"
	}

	return s.id
}

// encode turns a struct value into a JSON document, with the tag names.
func (s *schema) encode(v any) (json.RawMessage, error) {
	rv := reflect.ValueOf(v)
	doc := make(map[string]any, len(s.fields))

	for _, f := range s.fields {
		fv := rv.Field(f.index)

		if f.name == s.id && fv.IsZero() || f.name == s.version {
			continue
		}

		doc[f.name] = fv.Interface()
	}

	return json.Marshal(doc)
}

// decode fills the struct dst from a JSON document. A field missing from the
// document keeps its zero value.
func (s *schema) decode(raw []byte, dst reflect.Value) error {
	var doc map[string]json.RawMessage

	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}

	for _, f := range s.fields {
		val, ok := doc[f.name]

		if !ok {
			continue
		}

		if err := json.Unmarshal(val, dst.Field(f.index).Addr().Interface()); err != nil {
			return fmt.Errorf("ndb: field %s: %w", f.name, err)
		}
	}

	return nil
}
