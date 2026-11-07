#!/usr/bin/env bash
# Reports with aggregate: $match, $group by hour with $dateTrunc, $sum, $avg, $sort, and a filtered count.
set -euo pipefail

. "$(dirname "$0")/../lib/ndb.sh"

# Seed the twelve orders once. A second run finds them and does nothing.
if [[ "$(ndb POST /orders/_count '{}' | jq -r '.count')" == "0" ]]; then
	ndb POST /orders/_bulk '{"docs":[
	  {"_id":"o01","created_at":"2026-11-01T09:05:00Z","status":"paid","amount":50},
	  {"_id":"o02","created_at":"2026-11-01T09:20:00Z","status":"paid","amount":70},
	  {"_id":"o03","created_at":"2026-11-01T09:45:00Z","status":"refunded","amount":30},
	  {"_id":"o04","created_at":"2026-11-01T10:02:00Z","status":"paid","amount":120},
	  {"_id":"o05","created_at":"2026-11-01T10:15:00Z","status":"paid","amount":80},
	  {"_id":"o06","created_at":"2026-11-01T10:30:00Z","status":"pending","amount":60},
	  {"_id":"o07","created_at":"2026-11-01T10:55:00Z","status":"paid","amount":160},
	  {"_id":"o08","created_at":"2026-11-01T11:05:00Z","status":"paid","amount":40},
	  {"_id":"o09","created_at":"2026-11-01T11:25:00Z","status":"refunded","amount":90},
	  {"_id":"o10","created_at":"2026-11-01T11:40:00Z","status":"paid","amount":60},
	  {"_id":"o11","created_at":"2026-11-01T11:50:00Z","status":"pending","amount":20},
	  {"_id":"o12","created_at":"2026-11-01T11:59:00Z","status":"paid","amount":110}
	]}' >/dev/null
fi

echo "orders: $(ndb POST /orders/_count '{}' | jq -r '.count')"

# region hourly
echo "paid orders per hour, best revenue first:"

ndb POST /orders/_aggregate '{"pipeline":[
  {"$match":{"status":"paid"}},
  {"$group":{
    "_id":{"$dateTrunc":{"date":"$created_at","unit":"hour"}},
    "orders":{"$count":{}},
    "revenue":{"$sum":"$amount"},
    "avg_ticket":{"$avg":"$amount"}
  }},
  {"$sort":{"revenue":-1}}
]}' | jq -r '.[] | "  \(._id) orders=\(.orders) revenue=\(.revenue) avg=\(.avg_ticket)"'
# endregion hourly

# region by-status
echo "by status:"

ndb POST /orders/_aggregate '{"pipeline":[
  {"$group":{"_id":"$status","orders":{"$count":{}},"revenue":{"$sum":"$amount"}}},
  {"$sort":{"_id":1}}
]}' | jq -r '.[] | "  \(._id + "        " | .[0:8]) orders=\(.orders) revenue=\(.revenue)"'
# endregion by-status

# region count
big=$(ndb POST /orders/_count '{"query":{"status":"paid","amount":{"$gte":100}}}' | jq -r '.count')

echo "paid orders of 100 or more: $big"
# endregion count
