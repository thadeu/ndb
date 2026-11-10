package server

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// The routes of the HTTP API 1.0. A path is cut into segments:
//
//	/_<system>[/{ref}]                 system: _health, _stats, _blobs...
//	/{coll}                            the collection
//	/{coll}/_<action>                  _search, _count, _bulk, _truncate...
//	/{coll}/_<action>/{id|field}       _doc/{id}, _update/{id}, _indexes/{field}
//
// A collection name never starts with "_", so the system and the actions
// never collide with data, and a document id always comes after an action,
// so it never collides with one. Anything else is 404 unknown_route; a known
// path with another method is 405 method_not_allowed.

// route is one method of one path shape.
type route struct {
	method string
	h      http.HandlerFunc
}

// router maps a path shape ("_search", "_doc/{}", "_health") to its routes.
type router struct {
	system map[string][]route
	coll   map[string][]route
}

// param names the third segment of an action.
var param = map[string]string{"_indexes": "field", "_blobs": "ref"}

func (rt *router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	segs, ok := segments(r.URL.EscapedPath())

	if !ok || len(segs) == 0 || len(segs) > maxSegments {
		unknownRoute(w)

		return
	}

	routes, ok := rt.match(r, segs)
	if !ok {
		unknownRoute(w)

		return
	}

	i := slices.IndexFunc(routes, func(x route) bool { return x.method == r.Method })
	if i < 0 {
		methodNotAllowed(w, routes)

		return
	}

	routes[i].h(w, r)
}

// match finds the routes of the path and sets its path values (coll, id,
// field, ref).
func (rt *router) match(r *http.Request, segs []string) ([]route, bool) {
	table := rt.coll
	rest := segs

	if strings.HasPrefix(segs[0], "_") {
		table = rt.system
	} else {
		r.SetPathValue("coll", segs[0])
		rest = segs[1:]
	}

	if len(rest) > 0 && rest[0] == "_streams" {
		return rt.matchStreams(r, rest)
	}

	key := ""

	switch len(rest) {
	case 0:
	case 1:
		key = rest[0]
	case 2:
		key = rest[0] + "/{}"

		name := param[rest[0]]
		if name == "" {
			name = "id"
		}

		r.SetPathValue(name, rest[1])
	default:
		return nil, false
	}

	routes, ok := table[key]

	r.SetPathValue("action", key)

	return routes, ok
}

// segments splits an escaped path and unescapes each segment, so an id may
// hold an escaped "/".
func segments(escaped string) ([]string, bool) {
	trimmed := strings.Trim(escaped, "/")

	if trimmed == "" {
		return nil, true
	}

	parts := strings.Split(trimmed, "/")

	for i, p := range parts {
		s, err := url.PathUnescape(p)
		if err != nil || s == "" {
			return nil, false
		}

		parts[i] = s
	}

	return parts, true
}

func unknownRoute(w http.ResponseWriter) {
	writeError(w, &apiError{status: http.StatusNotFound, code: "unknown_route", msg: "no such route; see the HTTP API reference"})
}

func methodNotAllowed(w http.ResponseWriter, routes []route) {
	methods := make([]string, len(routes))

	for i, x := range routes {
		methods[i] = x.method
	}

	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, &apiError{status: http.StatusMethodNotAllowed, code: "method_not_allowed", msg: "use " + strings.Join(methods, " or ")})
}

// maxSegments is the longest path: /{s}/_streams/groups/{g}/consumers/{c}.
const maxSegments = 6

// matchStreams matches the routes under /{s}/_streams by position: rest is
// "_streams", "groups", the group, "consumers", the consumer. So the key of
// /{s}/_streams/groups/g/consumers/c is "_streams/groups/{}/consumers/{}",
// and a group or consumer may have any name ("groups", "read", ...).
func (rt *router) matchStreams(r *http.Request, rest []string) ([]route, bool) {
	parts := make([]string, len(rest))

	copy(parts, rest)

	if len(rest) > 2 && rest[1] == "groups" {
		parts[2] = "{}"

		r.SetPathValue("group", rest[2])
	}

	if len(rest) > 4 && rest[1] == "groups" && rest[3] == "consumers" {
		parts[4] = "{}"

		r.SetPathValue("consumer", rest[4])
	}

	key := strings.Join(parts, "/")

	routes, ok := rt.coll[key]

	r.SetPathValue("action", key)

	return routes, ok
}
