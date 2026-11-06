package ndb

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"sync"
	"time"
)

var idGen struct {
	mu      sync.Mutex
	ms      int64
	counter uint16
}

// NewID returns a new UUIDv7 (RFC 9562), lowercase and hyphenated, the same
// kind of id that the daemon generates. Ids from one process sort in the
// order of their creation.
func NewID() string {
	var b [16]byte

	ms, counter := nextIDStamp(time.Now().UnixMilli())

	var ms8 [8]byte

	binary.BigEndian.PutUint64(ms8[:], uint64(ms))
	copy(b[:6], ms8[2:])

	_, _ = rand.Read(b[8:])

	b[6] = 0x70 | byte(counter>>8)
	b[7] = byte(counter)
	b[8] = 0x80 | b[8]&0x3f

	var out [36]byte

	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])

	return string(out[:])
}

// nextIDStamp returns the millisecond and the 12-bit counter of the next id.
// The counter starts at a random value in each new millisecond; on overflow
// the millisecond moves forward.
func nextIDStamp(now int64) (int64, uint16) {
	idGen.mu.Lock()
	defer idGen.mu.Unlock()

	if now > idGen.ms {
		idGen.ms = now
		idGen.counter = seedIDCounter()

		return idGen.ms, idGen.counter
	}

	if idGen.counter == 0x0fff {
		idGen.ms++
		idGen.counter = seedIDCounter()

		return idGen.ms, idGen.counter
	}

	idGen.counter++

	return idGen.ms, idGen.counter
}

func seedIDCounter() uint16 {
	var r [2]byte

	_, _ = rand.Read(r[:])

	return binary.BigEndian.Uint16(r[:]) & 0x07ff
}
