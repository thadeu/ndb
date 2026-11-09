package segment

import (
	"math"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/bloom"
)

// memBloom is the bloom filter of one column chunk, read into memory once.
// The filter of parquet-go reads the file on each Check: one pread of 32
// bytes per lookup and segment, which with concurrent reads kept the
// threads in syscalls and in the scheduler.
type memBloom struct {
	filter bloom.SplitBlockFilter
}

// loadBloom reads the bits of a file filter. nil when it cannot be read.
func loadBloom(bf parquet.BloomFilter) *memBloom {
	buf := make([]byte, bf.Size())

	n, err := bf.ReadAt(buf, 0)

	if err != nil || int64(n) != bf.Size() || len(buf)%bloom.BlockSize != 0 {
		return nil
	}

	return &memBloom{filter: bloom.MakeSplitBlockFilter(buf)}
}

// Check tells whether v may be in the filter.
func (b *memBloom) Check(v parquet.Value) bool {
	return b.filter.Check(bloomHash(v))
}

// bloomHash is the hash of v in a parquet bloom filter: XXH64 of its plain
// encoding, as parquet-go computes it.
func bloomHash(v parquet.Value) uint64 {
	h := bloom.XXH64{}

	switch v.Kind() {
	case parquet.Boolean:
		var b uint8

		if v.Boolean() {
			b = 1
		}

		return h.Sum64Uint8(b)
	case parquet.Int32:
		return h.Sum64Uint32(uint32(v.Int32()))
	case parquet.Float:
		return h.Sum64Uint32(math.Float32bits(v.Float()))
	case parquet.Int64:
		return h.Sum64Uint64(uint64(v.Int64()))
	case parquet.Double:
		return h.Sum64Uint64(math.Float64bits(v.Double()))
	default:
		return h.Sum64(v.ByteArray())
	}
}
