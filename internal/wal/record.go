package wal

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"strconv"
	"time"
	"unicode/utf8"
)

// ErrCorrupt: the line is cut, damaged, or not a WAL record.
var ErrCorrupt = errors.New("wal: corrupt record")

type OpKind string

const (
	Put OpKind = "put"
	Del OpKind = "del"
)

// Op is one document change inside a transaction.
type Op struct {
	Kind OpKind
	ID   string
	V    uint64
	Doc  json.RawMessage
}

// Record is one transaction = one WAL line.
type Record struct {
	Tx  uint64
	TS  time.Time
	Ops []Op
}

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// crcSuffix is `,"_crc":"` + 8 hex + `"}`.
const (
	crcPrefix = `,"_crc":"`
	crcLen    = len(crcPrefix) + 8 + 2
)

// Encode appends the line for rec to dst, without the final '\n'.
// Field order is fixed, so the CRC covers the exact bytes before `,"_crc"`.
func Encode(dst []byte, rec Record) ([]byte, error) {
	if len(rec.Ops) == 0 {
		return dst, errors.New("wal: record without ops")
	}

	start := len(dst)

	dst = append(dst, `{"_tx":`...)
	dst = strconv.AppendUint(dst, rec.Tx, 10)
	dst = append(dst, `,"_ts":"`...)
	dst = rec.TS.UTC().AppendFormat(dst, time.RFC3339Nano)
	dst = append(dst, '"')

	var err error

	if len(rec.Ops) == 1 {
		dst = append(dst, ',')
		dst, err = appendOpFields(dst, rec.Ops[0])
	} else {
		dst, err = appendOps(dst, rec.Ops)
	}

	if err != nil {
		return dst[:start], err
	}

	sum := crc32.Checksum(dst[start:], crcTable)

	dst = append(dst, crcPrefix...)
	dst = hex.AppendEncode(dst, binaryBE(sum))
	dst = append(dst, `"}`...)

	return dst, nil
}

func appendOps(dst []byte, ops []Op) ([]byte, error) {
	dst = append(dst, `,"ops":[`...)

	for i, op := range ops {
		if i > 0 {
			dst = append(dst, ',')
		}

		dst = append(dst, '{')

		var err error

		dst, err = appendOpFields(dst, op)
		if err != nil {
			return dst, err
		}

		dst = append(dst, '}')
	}

	return append(dst, ']'), nil
}

func appendOpFields(dst []byte, op Op) ([]byte, error) {
	if op.ID == "" || !utf8.ValidString(op.ID) {
		return dst, errors.New("wal: op needs a valid UTF-8 id")
	}

	if op.Kind != Put && op.Kind != Del {
		return dst, fmt.Errorf("wal: unknown op %q", op.Kind)
	}

	id, _ := json.Marshal(op.ID)

	dst = append(dst, `"_op":"`...)
	dst = append(dst, op.Kind...)
	dst = append(dst, `","_id":`...)
	dst = append(dst, id...)
	dst = append(dst, `,"_v":`...)
	dst = strconv.AppendUint(dst, op.V, 10)

	if op.Kind == Del {
		return dst, nil
	}

	doc, err := compactObject(op.Doc)
	if err != nil {
		return dst, err
	}

	dst = append(dst, `,"doc":`...)

	return append(dst, doc...), nil
}

func compactObject(doc json.RawMessage) ([]byte, error) {
	var buf bytes.Buffer

	if err := json.Compact(&buf, doc); err != nil {
		return nil, fmt.Errorf("wal: doc is not valid JSON: %w", err)
	}

	if buf.Len() == 0 || buf.Bytes()[0] != '{' {
		return nil, errors.New("wal: doc must be a JSON object")
	}

	return buf.Bytes(), nil
}

func binaryBE(v uint32) []byte {
	return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}

type wireOp struct {
	Op  OpKind          `json:"_op"`
	ID  *string         `json:"_id"`
	V   uint64          `json:"_v"`
	Doc json.RawMessage `json:"doc"`
}

type wireRecord struct {
	wireOp

	Tx  uint64    `json:"_tx"`
	TS  time.Time `json:"_ts"`
	Ops []wireOp  `json:"ops"`
}

// Decode checks the CRC and parses one line (without '\n').
func Decode(line []byte) (Record, error) {
	if len(line) < crcLen+2 {
		return Record{}, ErrCorrupt
	}

	body := line[:len(line)-crcLen]
	suffix := line[len(line)-crcLen:]

	if !bytes.HasPrefix(suffix, []byte(crcPrefix)) || !bytes.HasSuffix(suffix, []byte(`"}`)) {
		return Record{}, ErrCorrupt
	}

	want, err := hex.DecodeString(string(suffix[len(crcPrefix) : len(crcPrefix)+8]))
	if err != nil || !bytes.Equal(want, binaryBE(crc32.Checksum(body, crcTable))) {
		return Record{}, ErrCorrupt
	}

	var w wireRecord

	if err := json.Unmarshal(line, &w); err != nil {
		return Record{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}

	rec := Record{Tx: w.Tx, TS: w.TS}

	if w.Ops == nil {
		w.Ops = []wireOp{w.wireOp}
	}

	for _, o := range w.Ops {
		if o.ID == nil || (o.Op != Put && o.Op != Del) {
			return Record{}, ErrCorrupt
		}

		rec.Ops = append(rec.Ops, Op{Kind: o.Op, ID: *o.ID, V: o.V, Doc: o.Doc})
	}

	return rec, nil
}
