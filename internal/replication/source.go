package replication

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Source is one owner that a copy pulls: where it is, its token, the
// database on the owner (Remote) and the database on the copy (Local).
type Source struct {
	// Base is the URL of the owner, http://host:port.
	Base string

	Token string

	// Remote and Local are database names; "default" is the data dir.
	Remote string
	Local  string
}

// Redacted is the URL of the source with the token hidden.
func (s Source) Redacted() string {
	u, err := url.Parse(s.Base)
	if err != nil {
		return ""
	}

	scheme := "tcp"

	if u.Scheme == "https" {
		scheme = "https"
	}

	out := scheme + "://"

	if s.Token != "" {
		out += ":***@"
	}

	out += u.Host + "/" + s.Remote

	if s.Local != s.Remote {
		out += "?as=" + url.QueryEscape(s.Local)
	}

	return out
}

// DefaultDatabase is the name of the database of the data dir.
const DefaultDatabase = "default"

// ParseSources reads NDB_REPLICA_OF: a comma list of
// tcp://:<token>@host:port[/<database>][?as=<database>] (https:// for an
// owner behind TLS). The path is the database on the owner (none:
// default); as is the database on the copy (none: the same name). Two
// sources into one database is an error. valid checks a database name.
func ParseSources(s string, valid func(string) bool) ([]Source, error) {
	var out []Source

	into := map[string]bool{}

	for raw := range strings.SplitSeq(s, ",") {
		raw = strings.TrimSpace(raw)

		if raw == "" {
			continue
		}

		src, err := parseSource(raw, valid)
		if err != nil {
			return nil, err
		}

		if into[src.Local] {
			return nil, fmt.Errorf("NDB_REPLICA_OF: two sources write into the database %s", src.Local)
		}

		into[src.Local] = true

		out = append(out, src)
	}

	return out, nil
}

func parseSource(raw string, valid func(string) bool) (Source, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Source{}, fmt.Errorf("NDB_REPLICA_OF: %w", err)
	}

	scheme := ""

	switch u.Scheme {
	case "tcp", "http":
		scheme = "http"
	case "https":
		scheme = "https"
	default:
		return Source{}, fmt.Errorf("NDB_REPLICA_OF: %s: use tcp://:<token>@host:port[/<database>][?as=<database>]", redact(u))
	}

	if u.Host == "" || u.Port() == "" {
		return Source{}, fmt.Errorf("NDB_REPLICA_OF: %s: the owner needs a host and a port", redact(u))
	}

	src := Source{Base: scheme + "://" + u.Host, Remote: strings.Trim(u.Path, "/"), Local: u.Query().Get("as")}

	if u.User != nil {
		src.Token, _ = u.User.Password()

		if src.Token == "" {
			src.Token = u.User.Username()
		}
	}

	if src.Remote == "" {
		src.Remote = DefaultDatabase
	}

	if src.Local == "" {
		src.Local = src.Remote
	}

	for _, name := range []string{src.Remote, src.Local} {
		if name != DefaultDatabase && !valid(name) {
			return Source{}, fmt.Errorf("NDB_REPLICA_OF: %s: %q is not a valid database name", redact(u), name)
		}
	}

	return src, nil
}

// redact hides the token of a URL for an error message.
func redact(u *url.URL) string {
	c := *u

	c.User = nil

	return c.String()
}

// ErrResync: the data of the copy is not a prefix of the data of the owner.
var ErrResync = errors.New("this database has writes that the owner does not have")
