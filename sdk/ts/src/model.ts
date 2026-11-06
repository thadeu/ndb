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
  Row,
  Schema,
  SortSpec,
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

export class Model<F extends Fields> {
  private ensured?: Promise<void>

  constructor(
    readonly db: Ndb,
    readonly name: string,
    readonly schema: Schema<F>,
  ) {}

  path(op?: string): string {
    const base = `/v1/c/${encodeURIComponent(this.name)}`

    return op ? `${base}/${op}` : base
  }

  ready(): Promise<void> {
    this.ensured ??= this.db.transport.request<void>('PUT', this.path(), this.catalog()).catch((err: unknown) => {
      this.ensured = undefined

      throw err
    })

    return this.ensured
  }

  async insert(docs: Input<F> | Input<F>[], opts: InsertOptions = {}): Promise<WriteResult[]> {
    await this.ready()

    const list = Array.isArray(docs) ? docs : [docs]
    const out = await this.db.transport.request<{ results: WriteResult[] }>('POST', this.path('insert'), {
      insert: list,
      expire_in: opts.expireIn,
    })

    return out.results
  }

  create(docs: Input<F> | Input<F>[], opts: InsertOptions = {}): Promise<WriteResult[]> {
    return this.insert(docs, opts)
  }

  // Atomic update. A string is an _id (POST /{_id}/update); an object is a
  // filter (POST /update). The filter and the operators are sent as one body;
  // the daemon matches and writes in one step on the newest state, so
  // concurrent updates never interleave. A version guard is a filter:
  // { _id: 'c1', _v: 7 }. Matching nothing is not an error: matched is 0.
  // many does not apply to an _id.
  async update(idOrFilter: string | Filter<F>, ops: UpdateOps<F>, opts: UpdateOptions = {}): Promise<UpdateResult<F>> {
    await this.ready()

    const byId = typeof idOrFilter === 'string'
    const body = mergeUpdate(byId ? {} : idOrFilter, ops)
    const query = writeQuery({ many: byId ? undefined : opts.many, upsert: opts.upsert, expire_in: opts.expireIn })
    const op = byId ? `${encodeURIComponent(idOrFilter)}/update` : 'update'
    const out = await this.db.transport.request<{ matched: number; modified: number; doc?: Record<string, unknown> }>(
      'POST',
      this.path(op) + query,
      body,
    )

    return { matched: out.matched, modified: out.modified, doc: out.doc && (this.decode(out.doc) as Row<F>) }
  }

  // Deletes by _id (a string), or the documents that the filter matches: one,
  // or every match with many (same rule as update).
  async delete(idOrFilter: string | Filter<F>, opts: DeleteOptions = {}): Promise<DeleteResult> {
    await this.ready()

    if (typeof idOrFilter === 'string') {
      return this.db.transport.request<DeleteResult>('DELETE', this.path(encodeURIComponent(idOrFilter)))
    }

    const filter = idOrFilter

    return this.db.transport.request<DeleteResult>('POST', this.path('delete') + writeQuery({ many: opts.many }), toWhere(filter))
  }

  // Makes a document expire, like EXPIRE in Redis: seconds or "30m". It throws
  // NotFoundError for a missing or expired document, and TtlError when the
  // schema has no expires field.
  expire(id: string, duration: Duration): Promise<ExpireResult> {
    return this.setExpiry({ _id: id, in: duration })
  }

  expireAt(id: string, at: Date): Promise<ExpireResult> {
    return this.setExpiry({ _id: id, at: at.toISOString() })
  }

  // Removes the expiry of a document (PERSIST in Redis).
  async persist(id: string): Promise<WriteResult> {
    await this.ready()

    return this.db.transport.request<WriteResult>('POST', this.path('persist'), { _id: id })
  }

  // The seconds left (rounded up), -1 when the document never expires, null
  // when it is missing or expired.
  async ttl(id: string): Promise<number | null> {
    await this.ready()

    try {
      const out = await this.db.transport.request<{ ttl: number }>('GET', this.path(`${encodeURIComponent(id)}/ttl`))

      return out.ttl
    } catch (err) {
      if (err instanceof NotFoundError) {
        return null
      }

      throw err
    }
  }

  private async setExpiry(body: Record<string, unknown>): Promise<ExpireResult> {
    await this.ready()

    const out = await this.db.transport.request<WriteResult & { expires_at: string }>('POST', this.path('expire'), body)

    return { _id: out._id, v: out.v, expiresAt: new Date(out.expires_at) }
  }

  // The document with this _id, or null.
  async get(id: string): Promise<Row<F> | null> {
    await this.ready()

    try {
      const doc = await this.db.transport.request<Record<string, unknown>>('GET', this.path(encodeURIComponent(id)))

      return this.decode(doc) as Row<F>
    } catch (err) {
      if (err instanceof NotFoundError) {
        return null
      }

      throw err
    }
  }

  query(): Query<F> {
    return new Query<F>(this)
  }

  where(w: Where<F>): Query<F> {
    return this.query().where(w)
  }

  not(w: Where<F>): Query<F> {
    return this.query().not(w)
  }

  or(...ws: Where<F>[]): Query<F> {
    return this.query().or(...ws)
  }

  sort(spec: SortSpec<F>): Query<F> {
    return this.query().sort(spec)
  }

  limit(n: number): Query<F> {
    return this.query().limit(n)
  }

  skip(n: number): Query<F> {
    return this.query().skip(n)
  }

  page(n: number, size: number): Query<F> {
    return this.query().page(n, size)
  }

  after(token: string): Query<F> {
    return this.query().after(token)
  }

  fields(...names: string[]): Query<F, Partial<Row<F>>> {
    return this.query().fields(...names)
  }

  all(): Promise<Row<F>[]> {
    return this.query().all()
  }

  count(): Promise<number> {
    return this.query().count()
  }

  first(): Promise<Row<F> | null> {
    return this.query().first()
  }

  iterate(): AsyncGenerator<Row<F>> {
    return this.query().iterate()
  }

  async aggregate<R = Record<string, unknown>>(
    pipeline: Record<string, unknown>[],
    opts: AggregateOptions = {},
  ): Promise<AggregateResult<R>> {
    await this.ready()

    const res = await this.db.transport.stream('POST', this.path('aggregate'), { pipeline, ...opts })
    const rows: R[] = []

    for await (const row of res.rows()) {
      rows.push(row as R)
    }

    return { rows, next: res.header('x-ndb-next') ?? '', total: Number(res.header('x-ndb-total') ?? 0) }
  }

  async *watch(consumer: string, opts: WatchOptions<F> = {}): AsyncGenerator<WatchEvent<Row<F>>> {
    await this.ready()

    const body = { consumer, from: opts.from, limit: opts.limit, where: opts.where === undefined ? undefined : toWhere(opts.where) }
    const res = await this.db.transport.stream('POST', this.path('watch'), body, opts.signal)

    for await (const ev of watchEvents(res.rows(), consumer)) {
      yield { ...ev, doc: ev.doc && (this.decode(ev.doc) as Row<F>) }
    }
  }

  async ack(event: Pick<WatchEvent<unknown>, 'consumer' | 'pos'>): Promise<void> {
    await this.db.transport.request('POST', this.path('ack'), { consumer: event.consumer, pos: event.pos })
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

function writeQuery(params: Record<string, boolean | number | string | undefined>): string {
  const q = new URLSearchParams()

  for (const [name, value] of Object.entries(params)) {
    if (value !== undefined && value !== false) {
      q.set(name, String(value))
    }
  }

  const text = q.toString()

  return text === '' ? '' : `?${text}`
}

// mergeUpdate puts the filter and the operators in the one body of the update
// call. An operator key without "$" would be read as a filter key by the
// daemon, so it is refused before anything is sent.
function mergeUpdate(filter: object, ops: object): Record<string, unknown> {
  for (const key of Object.keys(ops)) {
    if (!key.startsWith('$')) {
      throw new InvalidUpdateError('invalid_update', `update operator "${key}" must start with $ (for example "$set")`)
    }
  }

  return { ...(toWhere(filter) as Record<string, unknown>), ...ops }
}
