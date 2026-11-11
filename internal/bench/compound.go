package bench

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// Compound indexes and $unwind, on a daemon of its own: orders of accounts,
// each with items. The same reads are timed twice: with an index of the
// account only, then with compound indexes (account + amount, account +
// status). Each read tells how many documents it examined, so the report
// shows what the compound index cuts. The budget is on the second run.

const (
	compoundOrders   = 60_000
	compoundAccounts = 60
	compoundBatch    = 2_000
)

var compoundStatuses = []string{"paid", "paid", "paid", "open", "void"}

func compoundPlan() []planned {
	sec := func(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

	return []planned{
		{"compound: load", sec(compoundOrders / 15_000)},
		{"compound: one index", sec(3)},
		{"compound: build", sec(3)},
		{"compound", sec(3)},
	}
}

func compoundPart(dir string) []string {
	d := &daemon{dir: filepath.Join(dir, "compound")}

	check(os.MkdirAll(d.dir, 0o755))
	d.start()

	defer d.stop()

	d.must("PUT", "/orders", `{"time":"created_at","indexes":["account"]}`)

	compoundLoad(d)

	lines := []string{"| *with an index of the account* | |"}
	lines = append(lines, timedExamined(d, "compound: one index", compoundProbes())...)

	prog.step("compound: build", nil)

	began := time.Now()

	d.must("PUT", "/orders/_indexes/account,amount", "")
	d.must("PUT", "/orders/_indexes/account,status", "")

	for time.Since(began) < time.Minute && bytes.Contains(d.must("GET", "/orders/_stats", ""), []byte(`"building"`)) {
		time.Sleep(50 * time.Millisecond)
	}

	prog.end(fmt.Sprintf("2 compound indexes in %.1f s", time.Since(began).Seconds()))

	lines = append(lines, fmt.Sprintf("| *2 compound indexes built on %d orders* | %.1f s |", compoundOrders, time.Since(began).Seconds()), "| *with account,amount and account,status* | |")

	return append(lines, timedExamined(d, "compound", compoundProbes())...)
}

// compoundLoad writes the orders in time order and seals them.
func compoundLoad(d *daemon) {
	var written atomic.Int64

	prog.step("compound: load", func() string { return fmt.Sprintf("%d of %d orders", written.Load(), compoundOrders) })

	r := rand.New(rand.NewPCG(26, 3))
	start := yesterday().AddDate(0, 0, -30)

	for from := 0; from < compoundOrders; from += compoundBatch {
		var b strings.Builder

		b.WriteString(`{"docs":[`)

		for i := from; i < from+compoundBatch; i++ {
			if i > from {
				b.WriteByte(',')
			}

			at := start.Add(time.Duration(i) * 30 * 24 * time.Hour / compoundOrders)

			fmt.Fprintf(&b, `{"_id":"o%06d","account":"acc%03d","status":%q,"amount":%d,"created_at":%q,"items":[`,
				i, r.IntN(compoundAccounts), compoundStatuses[r.IntN(len(compoundStatuses))], r.IntN(10_000), at.Format(time.RFC3339))

			for k := range 1 + r.IntN(4) {
				if k > 0 {
					b.WriteByte(',')
				}

				fmt.Fprintf(&b, `{"sku":"sku%02d","qty":%d}`, r.IntN(40), 1+r.IntN(9))
			}

			b.WriteString(`]}`)
		}

		b.WriteString(`]}`)

		d.must("POST", "/orders/_bulk", b.String())
		written.Add(compoundBatch)
	}

	d.must("POST", "/orders/_compact", "")
	d.settle("/orders")

	prog.end(fmt.Sprintf("%d orders of %d accounts", compoundOrders, compoundAccounts))
}

// compoundProbes are reads that bind the account and one more field, and
// the same with an $unwind of the items.
func compoundProbes() []probe {
	agg := func(name, pipeline string) probe {
		return probe{name, "POST", "/orders/_aggregate", `{"pipeline":` + pipeline + `}`}
	}

	return []probe{
		{"an account, amounts of a 1% range", "POST", "/orders/_search", `{"query":{"account":"acc007","amount":{"$gte":100,"$lt":200}},"limit":100}`},
		{"3 accounts, low amounts", "POST", "/orders/_search", `{"query":{"account":{"$in":["acc001","acc007","acc042"]},"amount":{"$lt":50}},"limit":100}`},
		{"an account, top 20 amounts over 9000", "POST", "/orders/_search", `{"query":{"account":"acc007","amount":{"$gte":9000}},"sort":{"amount":-1},"limit":20}`},
		{"an account, 20 newest void orders", "POST", "/orders/_search", `{"query":{"account":"acc007","status":"void"},"sort":{"created_at":-1},"limit":20}`},
		{"count of the open orders of an account", "POST", "/orders/_count", `{"query":{"account":"acc007","status":"open"}}`},
		agg("units per sku of the void orders of an account ($unwind)", `[{"$match":{"account":"acc007","status":"void"}},{"$unwind":"$items"},{"$group":{"_id":"$items.sku","qty":{"$sum":"$items.qty"}}},{"$sort":{"qty":-1}},{"$limit":10}]`),
		agg("units per sku of the big orders of an account ($unwind)", `[{"$match":{"account":"acc007","amount":{"$gte":9000}}},{"$unwind":"$items"},{"$match":{"items.qty":{"$gte":5}}},{"$group":{"_id":"$items.sku","qty":{"$sum":"$items.qty"},"orders":{"$distinct":"$_id"}}}]`),
	}
}

// timedExamined is timed with the plan and the documents examined of each
// read, from the headers of one more call ("" when the route gives none).
func timedExamined(d *daemon, part string, probes []probe) []string {
	lines := timed(d, part, probes)

	for i, p := range probes {
		_, _, head, _ := d.do(p.method, p.path, []byte(p.body))

		if plan := head.Get("X-Ndb-Plan"); plan != "" {
			lines[i] = strings.TrimSuffix(lines[i], " |") + fmt.Sprintf("; %s, %s examined |", plan, head.Get("X-Ndb-Examined"))
		}
	}

	return lines
}
