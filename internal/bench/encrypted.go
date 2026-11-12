package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The cost of an encrypted field, on a daemon of its own: the same documents,
// with a 2 KB .pem, are written and read in a plain collection and in one
// where the .pem is encrypted, one after the other, so both see the same
// disk and CPU. The budget is the extra time of the encrypted median.
const encryptedDocs = 2_000

func encryptedPlan() []planned {
	return []planned{{"encrypted", 8 * time.Second}}
}

func encryptedPart(dir string) []string {
	d := &daemon{dir: filepath.Join(dir, "encrypted")}

	check(os.MkdirAll(d.dir, 0o755))
	d.start()

	defer d.stop()

	d.must("PUT", "/keys_plain", `{}`)
	d.must("PUT", "/keys_sealed", `{"encrypted":["pem"]}`)

	prog.step("encrypted", nil)

	pem := strings.Repeat("A", 2048)

	var writes, reads [2][]time.Duration

	for i := range encryptedDocs {
		for k, coll := range []string{"keys_plain", "keys_sealed"} {
			_, _, _, took := d.do("POST", "/"+coll+"/_doc", fmt.Appendf(nil, `{"_id":"k%05d","pem":%q}`, i, pem))
			writes[k] = append(writes[k], took)
		}
	}

	for i := range encryptedDocs {
		for k, coll := range []string{"keys_plain", "keys_sealed"} {
			_, _, _, took := d.do("GET", fmt.Sprintf("/%s/_doc/k%05d?decrypt=1", coll, i), nil)
			reads[k] = append(reads[k], took)
		}
	}

	w := [2]time.Duration{middle(writes[0]), middle(writes[1])}
	r := [2]time.Duration{middle(reads[0]), middle(reads[1])}

	record("encrypted.write_pct", overPct(w[0], w[1]))
	record("encrypted.read_pct", overPct(r[0], r[1]))

	prog.end(fmt.Sprintf("%d documents with a 2 KB .pem", encryptedDocs))

	return []string{
		fmt.Sprintf("| write of a doc with a 2 KB .pem, plain / encrypted (median) | %.2f / %.2f ms |", ms(w[0]), ms(w[1])),
		fmt.Sprintf("| read of it by _id, plain / encrypted (median) | %.2f / %.2f ms |", ms(r[0]), ms(r[1])),
	}
}

func middle(ds []time.Duration) time.Duration {
	s := slices.Clone(ds)

	slices.Sort(s)

	return s[len(s)/2]
}

// overPct is how much slower b is than a, in percent (negative is faster).
func overPct(a, b time.Duration) float64 {
	return (float64(b)/float64(a) - 1) * 100
}
