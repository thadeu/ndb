export interface TypeMap {
  string: string
  number: number
  boolean: boolean
  date: Date
  json: unknown
}

export type FieldType = keyof TypeMap

export interface FieldDef {
  type: FieldType
  index?: boolean
  unique?: boolean
  optional?: boolean

  // The daemon encrypts the field before it stores it. A filter on it is
  // only $exists, unless it is blind.
  encrypted?: boolean

  // An encrypted field that a filter finds by value ($eq, $in, $ne, $nin),
  // after this normalization: 'digits' for a CPF, 'lower' for an email.
  blind?: 'exact' | 'digits' | 'lower'
}

export type Fields = Record<string, FieldDef>

export interface Schema<F extends Fields> {
  time?: Extract<keyof F, string>

  // The date field that holds when a document expires. Needed by expireIn,
  // expire, persist and ttl.
  expires?: Extract<keyof F, string>

  // Compound indexes, each one 2 to 4 fields in order: [['account_id',
  // 'created_at']] serves equality on the first fields and a range on the
  // next one. One field: index: true on the field.
  indexes?: Extract<keyof F, string>[][]

  // Compound unique keys: one live document per tuple of values. A document
  // that misses one of the fields is not checked.
  unique?: Extract<keyof F, string>[][]
  fields: F
}

type ValueOf<D extends FieldDef> = TypeMap[D['type']]

type OptionalKeys<F extends Fields> = { [K in keyof F]: F[K] extends { optional: true } ? K : never }[keyof F]

export type Doc<F extends Fields> = {
  [K in Exclude<keyof F, OptionalKeys<F>>]: ValueOf<F[K]>
} & {
  [K in OptionalKeys<F>]?: ValueOf<F[K]>
}

// _id is the primary key of every document. Leave it out on insert and the
// daemon makes a UUIDv7.
export type Input<F extends Fields> = { [K in keyof F]?: ValueOf<F[K]> } & { _id?: string }

// _coll is set on rows read through a collection pattern ("logs-2026-*"): the
// name of the collection the row came from.
export type Row<F extends Fields> = Doc<F> & { _id: string; _v: number; _coll?: string }

export interface Ops<V> {
  $eq?: V
  $ne?: V
  $gt?: V
  $gte?: V
  $lt?: V
  $lte?: V
  $in?: V[]
  $nin?: V[]
  $exists?: boolean
  $regex?: string | RegExp

  // i, m, s and x. A RegExp in $regex brings its own flags: use one or the other.
  $options?: string

  // One element of the array matches the whole filter: field keys for an
  // array of objects ({ sku: 'a', qty: { $gt: 2 } }), operators for an array
  // of scalars ({ $gte: 10, $lt: 20 }).
  $elemMatch?: Record<string, unknown>
}

type Operand<V> = V extends Date ? Date | string : V

type RegexOperand<V> = V extends string ? RegExp : never

type JsonKeys<F extends Fields> = Extract<{ [K in keyof F]: F[K]['type'] extends 'json' ? K : never }[keyof F], string>

export type Where<F extends Fields> = {
  [K in keyof F]?: Operand<ValueOf<F[K]>> | Ops<Operand<ValueOf<F[K]>>> | RegexOperand<ValueOf<F[K]>>
} & {
  _id?: string | Ops<string>
} & {
  [P in `${JsonKeys<F>}.${string}`]?: unknown
}

export type SortSpec<F extends Fields> = Partial<Record<Extract<keyof F, string> | '_id', 1 | -1>>

// A blob reference, the value of a document field that holds bytes stored
// with putBlob.
export interface Blob {
  $blob: string
  size: number
  type?: string
}

export interface WriteResult {
  _id: string
  _v: number
}

// A duration for the daemon: seconds, or a string such as "90m", "1h30m", "1d".
export type Duration = number | string

// The filter of update and delete: a where object, plus the version "_v" (a
// guard for optimistic concurrency) and the boolean operators.
export type Filter<F extends Fields> = Where<F> & {
  _v?: number | Ops<number>
  $and?: Filter<F>[]
  $or?: Filter<F>[]
  $nor?: Filter<F>[]
}

// The write operators of an update. Every key of a field map is a field name
// or a dot path. Keys of this object must start with "$".
export interface UpdateOps<F extends Fields = Fields> {
  $set?: Input<F> & Record<string, unknown>

  // Like $set, but only when an upsert creates the document; ignored on a match.
  $setOnInsert?: Input<F> & Record<string, unknown>

  $unset?: Record<string, true | ''>

  // Negative numbers subtract. A missing field counts as 0.
  $inc?: Record<string, number>

  // A value is appended; { $each: [...] } appends many. A missing field
  // becomes a new array.
  $push?: Record<string, unknown>

  // A value removes every equal element; a condition ({ $gte: 5 }, or
  // { status: 'done' } for objects) removes the elements that match it.
  $pull?: Record<string, unknown>

  // Set the field when it is missing or the value is lower (higher).
  $min?: Record<string, number | string>
  $max?: Record<string, number | string>
}

// Options of update by _id.
export interface UpdateOptions<F extends Fields = Fields> {
  // Extra conditions on the same document, such as { _v: 7 } (optimistic
  // concurrency): matched is 0 when they fail.
  guard?: Filter<F>

  // Create the document with this _id when it is missing.
  upsert?: boolean

  // The daemon sets the expires field to now + expireIn, in the same write.
  expireIn?: Duration
}

// Options of updateBy.
export interface UpdateByOptions {
  // Update every match. The query needs _id or an indexed field
  // (UnindexedManyError otherwise).
  many?: boolean

  // Create the document from the equality fields of the query when nothing
  // matches.
  upsert?: boolean

  // The daemon sets the expires field to now + expireIn, in the same write.
  expireIn?: Duration
}

export interface DeleteOptions {
  many?: boolean
}

// matched is 0 when the query matched nothing (that is not an error), and
// modified is 0 when the operators left the document as it was. doc is the
// new document with its version; it is absent for many.
export interface UpdateResult<F extends Fields = Fields> {
  matched: number
  modified: number
  doc?: Row<F>
}

export interface RetryOptions {
  // How many times withRetry tries (default 5).
  retries?: number
}

export interface LockOptions {
  // How long the daemon keeps the lock when it is not released: seconds, or
  // a duration such as "30s" (default 30 s, up to 5 min). Set it over the
  // longest time the function can take.
  ttl?: Duration

  // How long to wait for another holder (default 10 s, up to 1 min). 0 does
  // not wait: a held document is LockedError at once.
  wait?: Duration
}

export interface DeleteResult {
  deleted: number
}

export interface InsertOptions {
  // Applies to every document of the call.
  expireIn?: Duration
}

export interface ExpireResult extends WriteResult {
  expiresAt: Date
}

export interface AggregateOptions {
  limit?: number
  skip?: number
  after?: string
  count?: boolean
}

// The whole body of a search, built elsewhere (a search form, a saved
// search). It is sent as is; the daemon validates it.
export interface SearchBody {
  query?: Record<string, unknown>
  fields?: string[]
  sort?: Record<string, 1 | -1>
  limit?: number
  skip?: number
  after?: string
  count?: boolean
}

// One page of a search. next is '' on the last page; total is set with count.
export interface SearchResult<R> {
  rows: R[]
  next: string
  total: number
}

export interface AggregateResult<R = Record<string, unknown>> {
  rows: R[]
  next: string
  total: number
}

export interface WatchOptions<F extends Fields = Fields> {
  from?: string
  limit?: number
  signal?: AbortSignal

  // Only the events whose document matches are delivered. A delete has no
  // document: it comes only if this stream saw an earlier matching put of
  // that id. The stored position still moves over the events left out.
  query?: Where<F>
}

// op "truncate": every document before this event is gone; _id is "".
export interface WatchEvent<R> {
  consumer: string
  pos: string
  op: 'put' | 'del' | 'truncate'
  _id: string
  _v: number
  doc: R | undefined
}

// The PITR window of a collection (GET /{coll}/_pitr). oldest and newest
// are RFC 3339 times; they are absent while nothing can be restored yet.
export interface PitrInfo {
  collection: string
  window: string
  oldest?: string
  newest?: string
  snapshots: number
  bytes: number
  dropped?: boolean
}

// A lookup of a search: the documents of another collection that a row
// points to. belongs_to: the fk is in this collection (an object or null);
// has_many: the fk is in the other one (an array); has_one: one of has_many.
// Build it with belongsTo, hasMany and hasOne.
export interface Relation {
  belongs_to?: string
  has_many?: string
  has_one?: string
  fk: string
  pk?: string
  query?: Record<string, unknown>
  sort?: Record<string, 1 | -1>
  limit?: number
  fields?: string[]
  lookup?: Record<string, Relation>
}

export type RelationOptions = Omit<Relation, 'belongs_to' | 'has_many' | 'has_one' | 'fk'>
