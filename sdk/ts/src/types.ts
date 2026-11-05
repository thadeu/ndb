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
  id?: Extract<keyof F, string>
  time?: Extract<keyof F, string>
  fields: F
}

type ValueOf<D extends FieldDef> = TypeMap[D['type']]

type OptionalKeys<F extends Fields> = { [K in keyof F]: F[K] extends { optional: true } ? K : never }[keyof F]

export type Doc<F extends Fields> = {
  [K in Exclude<keyof F, OptionalKeys<F>>]: ValueOf<F[K]>
} & {
  [K in OptionalKeys<F>]?: ValueOf<F[K]>
}

export type Input<F extends Fields> = { [K in keyof F]?: ValueOf<F[K]> }

export type Row<F extends Fields> = Doc<F> & { _v: number }

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
  [P in `${JsonKeys<F>}.${string}`]?: unknown
}

export type SortSpec<F extends Fields> = Partial<Record<Extract<keyof F, string>, 1 | -1>>

export interface WriteResult {
  id: string
  v: number
}

export interface WriteOptions {
  ifVersion?: number
}

export interface UpdateOptions extends WriteOptions {
  unset?: string[]
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
  id: string
  v: number
  doc: R | undefined
}
