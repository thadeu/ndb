import { InvalidUpdateError, NotFoundError } from './errors.js'
import type { Ndb } from './ndb.js'
import { Query } from './query.js'
import type {
  AggregateOptions,
  AggregateResult,
  DeleteOptions,
  DeleteResult,
  Duration,
  ExpireResult,
  Fields,
  Filter,
  Input,
  InsertOptions,
  PitrInfo,
  Row,
  Schema,
  SearchBody,
  SearchResult,
  SortSpec,
  UpdateByOptions,
  UpdateOps,
  UpdateOptions,
  UpdateResult,
  WatchEvent,
  WatchOptions,
  WriteResult,
  Where,
} from './types.js'
import { watchEvents } from './watch.js'
import { toWhere } from './where.js'

type UpdateAnswer = { matched: number; modified: number; doc?: Record<string, unknown> }

// One collection of the daemon. The first call declares it (PUT /{coll}) with
// the time, expires, index and unique settings of the schema. A name with a
// "*" at the end is a collection pattern ("logs-2026-*"): it reads every
// collection whose name starts with the prefix, declares nothing, and its
// writes fail with pattern_read_only.
export class Collection<F extends Fields> {
  private ensured?: Promise<void>

  constructor(
    readonly db: Ndb,
    readonly name: string,
    readonly schema: Schema<F>,
  ) {}

  // The path of the collection, or of one of its actions ("_search").
  path(action?: string): string {
    const base = `/${encodeURIComponent(this.name)}`

    return action ? `${base}/${action}` : base
  }

  get isPattern(): boolean {
    return this.name.includes('*')
  }

  ready(): Promise<void> {
    if (this.isPattern) return Promise.resolve()

    this.ensured ??= this.db.transport.request<void>('PUT', this.path(), this.catalog()).catch((err: unknown) => {
      this.ensured = undefined

      throw err
    })

    return this.ensured
  }

  // One document (POST /{coll}/_doc), or many in one transaction, all or
  // nothing (POST /{coll}/_bulk). expireIn applies to every document.
  insert(doc: Input<F>, opts?: InsertOptions): Promise<WriteResult>
  insert(docs: Input<F>[], opts?: InsertOptions): Promise<WriteResult[]>
  async insert(docs: Input<F> | Input<F>[], opts: InsertOptions = {}): Promise<WriteResult | WriteResult[]> {
    if (!Array.isArray(docs) && opts.expireIn === undefined) {
      return this.send<WriteResult>('POST', '_doc', docs)
    }

    const list = Array.isArray(docs) ? docs : [docs]
    const out = await this.send<{ results: WriteResult[] }>('POST', '_bulk', { docs: list, expire_in: opts.expireIn })

    return Array.isArray(docs) ? out.results : out.results[0]!
  }

  // Writes a document by its _id: creates it when the _id is free, and
  // merges its fields into the stored document when it exists (the other
  // fields stay), like doc_as_upsert in Elasticsearch. No _id is an insert.
  async upsert(doc: Input<F>, opts: InsertOptions = {}): Promise<WriteResult> {
    return (await this.upsertAll([doc], opts))[0]!
  }

  // upsert for many documents in one transaction, all or nothing
  // (POST /{coll}/_bulk with upsert).
  async upsertAll(docs: Input<F>[], opts: InsertOptions = {}): Promise<WriteResult[]> {
    const out = await this.send<{ results: WriteResult[] }>('POST', '_bulk', { docs, expire_in: opts.expireIn, upsert: true })

    return out.results
  }

  // Applies ops to the document with this _id, in one atomic step
  // (POST /{coll}/_update/{id}). guard adds conditions on the same document,
  // such as { _v: 7 }: matched is 0 when it fails. Matching nothing is not an
  // error.
  async update(id: string, ops: UpdateOps<F>, opts: UpdateOptions<F> = {}): Promise<UpdateResult<F>> {
    const out = await this.send<UpdateAnswer>('POST', docPath('_update', id), {
      query: opts.guard === undefined ? undefined : toWhere(opts.guard),
      update: checkOps(ops),
      upsert: opts.upsert,
      expire_in: opts.expireIn,
    })

    return this.updated(out)
  }

  // The atomic update by query (POST /{coll}/_update_by_query): the daemon
  // matches and writes in one step on the newest state, so concurrent updates
  // never interleave. Without many it updates the first match; many updates
  // every match and needs _id or an indexed field in the query.
  async updateBy(query: Filter<F>, ops: UpdateOps<F>, opts: UpdateByOptions = {}): Promise<UpdateResult<F>> {
    const out = await this.send<UpdateAnswer>('POST', '_update_by_query', {
      query: toWhere(query),
      update: checkOps(ops),
      many: opts.many,
      upsert: opts.upsert,
      expire_in: opts.expireIn,
    })

    return this.updated(out)
  }

  // Deletes the document with this _id (DELETE /{coll}/_doc/{id}).
  delete(id: string): Promise<DeleteResult> {
    return this.send<DeleteResult>('DELETE', docPath('_doc', id))
  }

  // Deletes the first document the query matches, or every match with many
  // (POST /{coll}/_delete_by_query, same rule as updateBy).
  deleteBy(query: Filter<F>, opts: DeleteOptions = {}): Promise<DeleteResult> {
    return this.send<DeleteResult>('POST', '_delete_by_query', { query: toWhere(query), many: opts.many })
  }

  // Removes every document and keeps the definition and indexes. The cost
  // does not depend on the number of documents. Watch consumers get one event
  // with op "truncate".
  async truncate(): Promise<void> {
    await this.db.transport.request('POST', this.path('_truncate') + this.confirm())
  }

  // Removes the collection: its documents, its definition and the positions
  // of its watch consumers. The next call declares it again.
  async drop(): Promise<void> {
    this.ensured = undefined

    await this.db.transport.request('DELETE', this.path() + this.confirm())
  }

  // Gives the collection the name `to`: documents, indexes and watch positions
  // move with it. With replace, an existing `to` goes aside first; without it,
  // an existing `to` throws CollectionExistsError. This object keeps the old
  // name: open the collection again under `to`.
  async rename(to: string, opts: { replace?: boolean } = {}): Promise<void> {
    this.ensured = undefined

    await this.db.transport.request('POST', this.path('_rename'), { to, replace: opts.replace ?? false })
  }

  // Makes the collection `to` hold this collection as it was at `at`, from the
  // PITR window of the daemon (1 day by default). The source is not touched.
  // Throws PitrError out of the window, CollectionExistsError when `to`
  // exists.
  async restore(opts: { at: Date; to: string }): Promise<void> {
    await this.db.transport.request('POST', this.path('_restore'), { at: opts.at.toISOString(), to: opts.to })
  }

  // The PITR window of the collection: from when a restore reaches, and the
  // disk it holds.
  async pitr(): Promise<PitrInfo> {
    return await this.db.transport.request<PitrInfo>('GET', this.path('_pitr'))
  }

  private confirm(): string {
    return `?confirm=${encodeURIComponent(this.name)}`
  }

  // Makes a document expire, like EXPIRE in Redis: seconds or "30m". It throws
  // NotFoundError for a missing or expired document, and TtlError when the
  // schema has no expires field.
  expire(id: string, duration: Duration): Promise<ExpireResult> {
    return this.setExpiry(id, { in: duration })
  }

  expireAt(id: string, at: Date): Promise<ExpireResult> {
    return this.setExpiry(id, { at: at.toISOString() })
  }

  // Removes the expiry of a document (PERSIST in Redis).
  persist(id: string): Promise<WriteResult> {
    return this.send<WriteResult>('POST', docPath('_persist', id))
  }

  // The seconds left (rounded up), -1 when the document never expires, null
  // when it is missing or expired.
  async ttl(id: string): Promise<number | null> {
    const out = await orNull(this.send<{ ttl: number }>('GET', docPath('_ttl', id)))

    return out === null ? null : out.ttl
  }

  private async setExpiry(id: string, body: Record<string, unknown>): Promise<ExpireResult> {
    const out = await this.send<WriteResult & { expires_at: string }>('POST', docPath('_expire', id), body)

    return { _id: out._id, _v: out._v, expiresAt: new Date(out.expires_at) }
  }

  // The document with this _id, or null.
  async get(id: string): Promise<Row<F> | null> {
    const doc = await orNull(this.send<Record<string, unknown>>('GET', docPath('_doc', id)))

    return doc === null ? null : (this.decode(doc) as Row<F>)
  }

  // A search (POST /{coll}/_search). No query matches every document.
  query(w?: Where<F>): Query<F> {
    const q = new Query<F>(this)

    return w === undefined ? q : q.query(w)
  }

  // A query built elsewhere, such as from a search form: it is not
  // type-checked and is sent as is; the daemon validates it.
  rawQuery(w: Record<string, unknown>): Query<F> {
    return new Query<F>(this).rawQuery(w)
  }

  not(w: Where<F>): Query<F> {
    return new Query<F>(this).not(w)
  }

  or(...ws: Where<F>[]): Query<F> {
    return new Query<F>(this).or(...ws)
  }

  sort(spec: SortSpec<F>): Query<F> {
    return new Query<F>(this).sort(spec)
  }

  limit(n: number): Query<F> {
    return new Query<F>(this).limit(n)
  }

  skip(n: number): Query<F> {
    return new Query<F>(this).skip(n)
  }

  page(n: number, size: number): Query<F> {
    return new Query<F>(this).page(n, size)
  }

  after(token: string): Query<F> {
    return new Query<F>(this).after(token)
  }

  fields(...names: string[]): Query<F, Partial<Row<F>>> {
    return new Query<F>(this).fields(...names)
  }

  all(): Promise<Row<F>[]> {
    return new Query<F>(this).all()
  }

  // How many documents match (POST /{coll}/_count).
  count(w?: Where<F>): Promise<number> {
    return this.query(w).count()
  }

  first(): Promise<Row<F> | null> {
    return new Query<F>(this).first()
  }

  // Every row, page by page.
  each(): AsyncGenerator<Row<F>> {
    return new Query<F>(this).each()
  }

  // Runs a whole search body built elsewhere, such as from a search form:
  // { query, sort, limit, after, count }. The body is sent as is (the daemon
  // validates it); rows are decoded like the rows of query().
  async search(body: SearchBody): Promise<SearchResult<Row<F>>> {
    await this.ready()

    const res = await this.db.transport.stream('POST', this.path('_search'), { ...body, versions: true })
    const rows: Row<F>[] = []

    for await (const raw of res.rows()) {
      rows.push(this.decode(raw as Record<string, unknown>) as Row<F>)
    }

    return { rows, next: res.header('x-ndb-next') ?? '', total: Number(res.header('x-ndb-total') ?? 0) }
  }

  async aggregate<R = Record<string, unknown>>(
    pipeline: Record<string, unknown>[],
    opts: AggregateOptions = {},
  ): Promise<AggregateResult<R>> {
    await this.ready()

    const res = await this.db.transport.stream('POST', this.path('_aggregate'), { pipeline, ...opts })
    const rows: R[] = []

    for await (const row of res.rows()) {
      rows.push(row as R)
    }

    return { rows, next: res.header('x-ndb-next') ?? '', total: Number(res.header('x-ndb-total') ?? 0) }
  }

  async *watch(consumer: string, opts: WatchOptions<F> = {}): AsyncGenerator<WatchEvent<Row<F>>> {
    await this.ready()

    const body = { consumer, from: opts.from, limit: opts.limit, query: opts.query === undefined ? undefined : toWhere(opts.query) }
    const res = await this.db.transport.stream('POST', this.path('_watch'), body, opts.signal)

    for await (const ev of watchEvents(res.rows(), consumer)) {
      yield { ...ev, doc: ev.doc && (this.decode(ev.doc) as Row<F>) }
    }
  }

  async ack(event: Pick<WatchEvent<unknown>, 'consumer' | 'pos'>): Promise<void> {
    await this.send('POST', '_ack', { consumer: event.consumer, pos: event.pos })
  }

  // Rows, indexes, files and bytes (GET /{coll}/_stats).
  stats(): Promise<Record<string, unknown>> {
    return this.send('GET', '_stats')
  }

  // Seals the memtable and merges every segment now (POST /{coll}/_compact).
  compact(): Promise<Record<string, unknown>> {
    return this.send('POST', '_compact')
  }

  // Adds a secondary index and builds it in the background. Returns the
  // indexed fields.
  async ensureIndex(field: string): Promise<string[]> {
    return (await this.send<{ indexes: string[] }>('PUT', docPath('_indexes', field))).indexes
  }

  async dropIndex(field: string): Promise<string[]> {
    return (await this.send<{ indexes: string[] }>('DELETE', docPath('_indexes', field))).indexes
  }

  decode(raw: Record<string, unknown>): Record<string, unknown> {
    const out = { ...raw }

    for (const [name, def] of Object.entries(this.schema.fields)) {
      const v = out[name]

      if (def.type === 'date' && typeof v === 'string') {
        out[name] = new Date(v)
      }
    }

    return out
  }

  // Declares the collection on first use, then sends one request to
  // /{coll}/{action}.
  private async send<T = unknown>(method: 'GET' | 'POST' | 'PUT' | 'DELETE', action: string, body?: unknown): Promise<T> {
    await this.ready()

    return this.db.transport.request<T>(method, this.path(action), body)
  }

  private updated(out: UpdateAnswer): UpdateResult<F> {
    return { matched: out.matched, modified: out.modified, doc: out.doc && (this.decode(out.doc) as Row<F>) }
  }

  private catalog(): Record<string, unknown> {
    const entries = Object.entries(this.schema.fields)

    return {
      time: this.schema.time,
      expires_field: this.schema.expires,
      indexes: entries.filter(([, d]) => d.index).map(([n]) => n),
      unique: entries.filter(([, d]) => d.unique).map(([n]) => n),
    }
  }
}

function docPath(action: string, id: string): string {
  return `${action}/${encodeURIComponent(id)}`
}

// An operator key without "$" is a mistake, so it is refused before
// anything is sent.
function checkOps<O extends object>(ops: O): O {
  for (const key of Object.keys(ops)) {
    if (!key.startsWith('$')) {
      throw new InvalidUpdateError('invalid_update', `update operator "${key}" must start with $ (for example "$set")`)
    }
  }

  return ops
}

async function orNull<T>(p: Promise<T>): Promise<T | null> {
  try {
    return await p
  } catch (err) {
    if (err instanceof NotFoundError) {
      return null
    }

    throw err
  }
}
