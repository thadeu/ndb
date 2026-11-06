#!/usr/bin/env bash
# Outbox: a producer inserts events, a consumer watches them with a filter and acks after the work.
# Delivery is at least once, so the work must be safe to repeat: a receipt per event id does that.
set -euo pipefail

. "$(dirname "$0")/../lib/ndb.sh"

# region work
# The work of the consumer. The receipt id is the event id, so a second delivery of
# the same event fails with duplicate_id (409) and the work is not done twice.
charge() {
	[[ "$(ndb_status POST /v1/c/receipts/insert "{\"insert\":[{\"_id\":\"$1\",\"done_at\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"}]}")" == "200" ]]
}
# endregion work

# Make the example repeatable: remove what an earlier run left.
for i in 1 2 3 4 5; do
	ndb DELETE "/v1/c/outbox/evt-$i" >/dev/null
	ndb DELETE "/v1/c/receipts/evt-$i" >/dev/null
done

# region produce
i=0

for topic in invoice.paid user.signup invoice.paid user.signup invoice.paid; do
	i=$((i + 1))

	ndb POST /v1/c/outbox/insert "{\"insert\":[{\"_id\":\"evt-$i\",\"topic\":\"$topic\",\"created_at\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"}]}" >/dev/null

	echo "produce evt-$i $topic"
done
# endregion produce

ack() {
	ndb POST /v1/c/outbox/ack "{\"consumer\":\"billing\",\"pos\":\"$1\"}" >/dev/null
}

# region consume
watch='{"consumer":"billing","limit":2,"where":{"topic":"invoice.paid"}}'

echo "consumer billing, run 1 (where topic = invoice.paid):"

while read -r pos id; do
	charge "$id" || true

	if [[ "$id" == "evt-3" ]]; then
		echo "  evt-3 charged, crash before ack"

		break
	fi

	ack "$pos"

	echo "  $id charged, acked"
done < <(ndb POST /v1/c/outbox/watch "$watch" | jq -r 'select(.pos) | "\(.pos) \(._id)"')
# endregion consume

# region restart
echo "consumer billing, run 2 (restart, resumes after the last ack):"

while read -r pos id; do
	if charge "$id"; then
		echo "  $id charged, acked"
	else
		echo "  $id delivered again, receipt exists, skipped, acked"
	fi

	ack "$pos"
done < <(ndb POST /v1/c/outbox/watch "$watch" | jq -r 'select(.pos) | "\(.pos) \(._id)"')
# endregion restart

echo "receipts: $(ndb_header X-Ndb-Total POST /v1/c/receipts/find '{"count":true,"limit":1}') (one per invoice, none twice)"
