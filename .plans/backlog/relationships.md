# Relationships (backlog, not in v1)

> Decision (2026-11-04): not now. This note keeps the idea so it is not lost.
> It is not part of `ndb-v1.md`.

## Why not now

- The current use cases (outbox, logs, call records, charts) read one collection at a time.
- Relationships are where the scope grows into a "full database":
  `includes`, then `joins(...).where`, then nested joins, then `counter_cache`.
- It can be added later with no breaking change: `include` is a new field in the
  query JSON, and `has_many`/`belongs_to` are new methods in the model layers.
  No v1 decision (WAL format, segments, indexes) blocks it.

## What v1 already gives

Two queries, with `$in` on an indexed field. Same cost as `includes`, written by the app:

```ruby
posts = Post.where(status: "published").limit(50).to_a
comments = Comment.where(post_id: posts.map(&:id)).group_by(&:post_id)
```

v1 requirement that keeps this door open: `$in` on an indexed field is one index
lookup per key, never a scan (phase 3).

## Draft design (for later)

- `include` in `find`, run **inside the daemon** (like MongoDB `$lookup`):
  one response, one snapshot, the same result in every language.

```json
{
  "where": { "status": "published" },
  "limit": 50,
  "include": {
    "comments": { "from": "comments", "local": "_id", "foreign": "post_id", "many": true, "limit": 10, "sort": { "created_at": -1 } },
    "author":   { "from": "users", "local": "author_id", "foreign": "_id", "many": false }
  }
}
```

- Execution: fetch the parent page, collect the distinct keys, one batched index
  lookup per relation, attach the results.
- Declaring `has_many :comments` creates the index on `comments.post_id` automatically.
- Bloom filters only help when the foreign key has no index (skip segments). They do not replace the index.
- Limits: per-parent `limit` on `has_many`; nested `include` up to 2 levels.
- Later: semi-join filters (`joins(:comments).where(comments: { approved: true })`).
- Out of scope: general joins with any condition.
- Lazy `post.comments` without `includes` works but warns (like Rails `strict_loading`).
- Prefer embedding when children are owned and bounded (order items, outbox steps).

## When to reconsider

When a real app (most likely a Rails app) writes the "two queries + `group_by`"
pattern in many places.
