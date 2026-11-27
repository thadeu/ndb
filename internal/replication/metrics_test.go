package replication_test

import (
	"strings"
	"testing"
)

func TestMetricsOfACopy(t *testing.T) {
	owner, cp, c := pair(t)

	owner.must("POST", "/a/_doc", "", `{"_id":"1"}`)

	sameData(t, owner.db, cp.db)
	eventually(t, "synced", c.Synced)

	body := string(cp.must("GET", "/_metrics", "", ""))

	for _, want := range []string{
		`ndb_replication_state{database="default",state="streaming"} 1`,
		`ndb_replication_lag_seconds{database="default"}`,
		`ndb_replication_lag_records{database="default"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no %s in the metrics", want)
		}
	}

	if strings.Contains(string(owner.must("GET", "/_metrics", "", "")), "ndb_replication_state") {
		t.Error("an owner shows the metrics of a copy")
	}
}
