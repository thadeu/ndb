package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestReplicationUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"nope"}, {"status", "promote"}} {
		if err := replicationCmd(args, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Fatalf("%v: err = %v", args, err)
		}
	}
}

func TestPrintReplication(t *testing.T) {
	st := replicationStatus{Databases: []replicaDB{
		{Database: "default", Role: "owner", Copies: []replicaPeer{{Addr: "10.0.0.2", Since: "now"}}},
		{Database: "app", Role: "copy", State: "down", Source: "tcp://:***@h:1/app", LastError: "refused"},
	}}

	var out bytes.Buffer

	if err := printReplication(&out, st); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"default: owner, term 0, 1 copies", "copy 10.0.0.2", "app: copy of tcp://:***@h:1/app, down", "not synced yet", "error: refused"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in:\n%s", want, out.String())
		}
	}
}
