package ndbredis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"

	ndb "github.com/thadeu/ndb/sdk/go"
)

// Client has the stream methods of go-redis (redis.StreamCmdable), on ndb
// streams. Make one with New. A Client is safe for many goroutines.
type Client struct {
	db *ndb.DB
}

// New returns the stream commands of go-redis on db. It sends nothing. The
// caller keeps db and closes it.
func New(db *ndb.DB) *Client {
	return &Client{db: db}
}

// ErrNotSupported is the cause of the error of a command, or of an option,
// that ndb streams do not have. Test it with errors.Is.
var ErrNotSupported = errors.New("ndbredis: not supported")

func notSupported(what string) error {
	return fmt.Errorf("%w: %s", ErrNotSupported, what)
}

// replyError is an error with the text of a Redis error reply. It is a
// redis.Error, and errors.Is finds the ndb error that caused it.
type replyError struct {
	text  string
	cause error
}

var _ redis.Error = (*replyError)(nil)

func (e *replyError) Error() string {
	return e.text
}

func (e *replyError) RedisError() {}

func (e *replyError) Unwrap() error {
	return e.cause
}

func reply(text string) error {
	return &replyError{text: text}
}

const (
	textNoKey      = "ERR no such key"
	textKeyNeeded  = "ERR The XGROUP subcommand requires the key to exist. Note that for CREATE you may want to use the MKSTREAM option to create an empty stream automatically."
	textBusyGroup  = "BUSYGROUP Consumer Group name already exists"
	textBadID      = "ERR Invalid stream ID specified as stream command argument"
	textSmallID    = "ERR The ID specified in XADD is equal or smaller than the target stream top item"
	textWrongType  = "WRONGTYPE Operation against a key holding the wrong kind of value"
	textUnbalanced = "ERR Unbalanced '%s' list of streams: for each stream key an ID or '%s' must be specified."
	textArgs       = "ERR wrong number of arguments for '%s' command"
)

// redisError gives an ndb error the text that Redis has for it. Other
// errors stay as they are.
func redisError(err error) error {
	text := ""

	switch {
	case err == nil:
		return nil
	case errors.Is(err, ndb.ErrGroupExists):
		text = textBusyGroup
	case errors.Is(err, ndb.ErrInvalidStreamID):
		text = textBadID
	case errors.Is(err, ndb.ErrStreamIDTooSmall):
		text = textSmallID
	case errors.Is(err, ndb.ErrNotAStream):
		text = textWrongType
	default:
		return err
	}

	return &replyError{text: text, cause: err}
}

// groupError is redisError for a command that needs the group: a group or
// a stream that does not exist is NOGROUP.
func groupError(err error, stream, group string) error {
	if !noGroup(err) {
		return redisError(err)
	}

	text := fmt.Sprintf("NOGROUP No such key '%s' or consumer group '%s'", stream, group)

	return &replyError{text: text, cause: ndb.ErrUnknownGroup}
}

func noGroup(err error) bool {
	return errors.Is(err, ndb.ErrUnknownGroup) || errors.Is(err, ndb.ErrUnknownCollection)
}

func noStream(err error) bool {
	return errors.Is(err, ndb.ErrUnknownCollection)
}

// doc is one entry as ndb keeps it: each field with its JSON value.
type doc = map[string]json.RawMessage

func (c *Client) stream(name string) *ndb.Stream[doc] {
	// StreamOf fails only for a struct that has bad tags.
	s, _ := ndb.StreamOf[doc](c.db, name)

	return s
}

// keepRef checks the mode of a trim or an append. ndb keeps the pending
// entries of what a trim deletes, which is KEEPREF, the default of Redis.
func keepRef(command, mode string) error {
	if mode == "" || strings.EqualFold(mode, "KEEPREF") {
		return nil
	}

	return notSupported(command + " " + strings.ToUpper(mode))
}

// trimLimit is the LIMIT of a trim with "~". Less than 0 is "no limit" in
// go-redis, which is 0 in ndb.
func trimLimit(n int64) int {
	return int(max(n, 0))
}

// ---- commands that ndb does not have

// XAckDel is not supported.
func (c *Client) XAckDel(ctx context.Context, stream string, group string, mode string, ids ...string) *redis.SliceCmd {
	cmd := redis.NewSliceCmd(ctx, "xackdel", stream, group, mode)

	cmd.SetErr(notSupported("XACKDEL"))

	return cmd
}

// XDelEx is not supported.
func (c *Client) XDelEx(ctx context.Context, stream string, mode string, ids ...string) *redis.SliceCmd {
	cmd := redis.NewSliceCmd(ctx, "xdelex", stream, mode)

	cmd.SetErr(notSupported("XDELEX"))

	return cmd
}

// XNack is not supported.
func (c *Client) XNack(ctx context.Context, a *redis.XNackArgs) *redis.IntCmd {
	cmd := redis.NewIntCmd(ctx, "xnack", a.Stream, a.Group, a.Mode)

	cmd.SetErr(notSupported("XNACK"))

	return cmd
}

// XCfgSet is not supported.
func (c *Client) XCfgSet(ctx context.Context, a *redis.XCfgSetArgs) *redis.StatusCmd {
	cmd := redis.NewStatusCmd(ctx, "xcfgset", a.Stream)

	cmd.SetErr(notSupported("XCFGSET"))

	return cmd
}
