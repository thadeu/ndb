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
}

export type Fields = Record<string, FieldDef>

export interface Schema<F extends Fields> {
  time?: Extract<keyof F, string>

  // The date field that holds when a document expires. Needed by expireIn,
  // expire, persist and ttl.
  expires?: Extract<keyof F, string>
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

export type Row<F extends Fields> = Doc<F> & { _id: string; _v: number }

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

export interface WriteResult {
  _id: string
  v: number
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

export interface UpdateOptions {
  // Update every match. The filter needs _id or an indexed field
  // (UnindexedManyError otherwise).
  many?: boolean

  // Create the document from the equality fields of the filter when nothing
  // matches.
  upsert?: boolean

  // The daemon sets the expires field to now + expireIn, in the same write.
  expireIn?: Duration
}

export interface DeleteOptions {
  many?: boolean
}

// matched is 0 when the filter matched nothing (that is not an error), and
// modified is 0 when the operators left the document as it was. doc is the
// new document with its version; it is absent for many.
export interface UpdateResult<F extends Fields = Fields> {
  matched: number
  modified: number
  doc?: Row<F>
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
  where?: Where<F>
}

export interface WatchEvent<R> {
  consumer: string
  pos: string
  op: 'put' | 'del'
  _id: string
  v: number
  doc: R | undefined
}
