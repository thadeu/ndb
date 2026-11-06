package collection

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"sync"
	"time"
)

// UUIDv7 ids (RFC 9562): 48-bit unix milliseconds, version 7, a 12-bit
// counter in rand_a, the variant bits and random bytes. The counter is
// seeded randomly for each new millisecond and grows inside the same
// millisecond, so the text order of ids is their creation order in this
// process.
var gen struct {
	mu      sync.Mutex
	ms      int64
	counter uint16
}

// NewID returns a new lowercase hyphenated UUIDv7.
func NewID() string {
	var b [16]byte

	ms, counter := nextStamp(time.Now().UnixMilli())

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

// nextStamp returns the millisecond and the counter of the next id. now
// going backwards is ignored: the last millisecond keeps counting.
func nextStamp(now int64) (int64, uint16) {
	gen.mu.Lock()
	defer gen.mu.Unlock()

	if now > gen.ms {
		gen.ms = now
		gen.counter = seedCounter()

		return gen.ms, gen.counter
	}

	if gen.counter == 0x0fff {
		gen.ms++
		gen.counter = seedCounter()

		return gen.ms, gen.counter
	}

	gen.counter++

	return gen.ms, gen.counter
}

// seedCounter is a random start in the lower half, so a burst has room
// before the counter overflows into the next millisecond.
func seedCounter() uint16 {
	var r [2]byte

	_, _ = rand.Read(r[:])

	return binary.BigEndian.Uint16(r[:]) & 0x07ff
}
