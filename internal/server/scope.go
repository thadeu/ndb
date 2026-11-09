package server

import (
	"path"
	"strings"
)

// Token scopes. A token of ndb.yml may read, write or administer some
// collections; the token of -token-file may do everything. Write includes
// read, admin includes both. The unix socket needs no token.

type access int

const (
	accessRead access = iota
	accessWrite
	accessAdmin
)

func (a access) String() string {
	return [...]string{"read", "write", "administer"}[a]
}

// Token is a bearer token and what it may do: the collections (names or
// globs) of each access level, or everything with Full.
type Token struct {
	Name   string
	Secret string
	Full   bool
	Read   []string
	Write  []string
	Admin  []string
}

// allows tells if the token may do need on coll. An empty coll is a system
// route: any collection at that level is enough. A pattern (logs-2026-*) is
// allowed by a glob that covers it, and a list (a,b) when each item is.
func (t Token) allows(coll string, need access) bool {
	if t.Full {
		return true
	}

	if strings.Contains(coll, ",") {
		for item := range strings.SplitSeq(coll, ",") {
			if item = strings.TrimSpace(item); item == "" || !t.allows(item, need) {
				return false
			}
		}

		return true
	}

	lists := [][]string{t.Admin}

	if need <= accessWrite {
		lists = append(lists, t.Write)
	}

	if need == accessRead {
		lists = append(lists, t.Read)
	}

	for _, list := range lists {
		for _, glob := range list {
			if coll == "" || covers(glob, coll) {
				return true
			}
		}
	}

	return false
}

// covers tells if glob matches the collection name, or every name of a
// collection pattern (a name with *).
func covers(glob, coll string) bool {
	if glob == "*" || glob == coll {
		return true
	}

	if strings.Contains(coll, "*") {
		prefix, ok := strings.CutSuffix(glob, "*")

		return ok && !strings.ContainsAny(prefix, "*?[") && strings.HasPrefix(strings.TrimSuffix(coll, "*"), prefix)
	}

	ok, _ := path.Match(glob, coll)

	return ok
}

// required is the collection and the access level a request needs. A system
// route has no collection ("").
func required(method string, segs []string) (string, access) {
	if len(segs) == 0 {
		return "", accessRead
	}

	if strings.HasPrefix(segs[0], "_") {
		switch segs[0] {
		case "_backup":
			return "*", accessAdmin
		case "_restore":
			return "", accessAdmin
		case "_blobs":
			if method == "POST" {
				return "", accessWrite
			}
		}

		return "", accessRead
	}

	coll, action := segs[0], ""

	if len(segs) > 1 {
		action = segs[1]
	}

	switch action {
	case "":
		if method == "GET" {
			return coll, accessRead
		}

		return coll, accessAdmin
	case "_truncate", "_indexes", "_compact", "_rename", "_restore":
		return coll, accessAdmin
	case "_doc":
		if method == "GET" {
			return coll, accessRead
		}

		return coll, accessWrite
	case "_bulk", "_update", "_expire", "_persist", "_update_by_query", "_delete_by_query", "_ack":
		return coll, accessWrite
	}

	return coll, accessRead
}
