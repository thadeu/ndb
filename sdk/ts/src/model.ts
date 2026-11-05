import type { Ndb } from './ndb.js'
import { Query } from './query.js'
import type {
  AggregateOptions,
  AggregateResult,
  Fields,
  Input,
  Row,
  Schema,
  SortSpec,
  UpdateOptions,
  WatchEvent,
  WatchOptions,
  WriteOptions,
  WriteResult,
  Where,
} from './types.js'
import { watchEvents } from './watch.js'

export class Model<F extends Fields> {
  private ensured?: Promise<void>

  constructor(
    readonly db: Ndb,
    readonly name: string,
    readonly schema: Schema<F>,
  ) {}

  get idField(): string {
    return this.schema.id ?? '_id'
  }

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

  async insert(docs: Input<F> | Input<F>[]): Promise<WriteResult[]> {
    await this.ready()

    const list = Array.isArray(docs) ? docs : [docs]
    const out = await this.db.transport.request<{ results: WriteResult[] }>('POST', this.path('insert'), {
      insert: list,
    })

    return out.results
  }

  create(docs: Input<F> | Input<F>[]): Promise<WriteResult[]> {
    return this.insert(docs)
  }

  async update(id: string, set: Input<F>, opts: UpdateOptions = {}): Promise<WriteResult> {
    await this.ready()

    const update = { id, set, unset: opts.unset, if_version: opts.ifVersion }

    return this.db.transport.request<WriteResult>('POST', this.path('update'), { update })
  }

  async delete(id: string, opts: WriteOptions = {}): Promise<void> {
    await this.ready()
    await this.db.transport.request('POST', this.path('delete'), { delete: { id, if_version: opts.ifVersion } })
  }

  find(id: string): Promise<Row<F> | null> {
    return this.query()
      .where({ [this.idField]: id } as Where<F>)
      .first()
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

  async *watch(consumer: string, opts: WatchOptions = {}): AsyncGenerator<WatchEvent<Row<F>>> {
    await this.ready()

    const body = { consumer, from: opts.from, limit: opts.limit }
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
      id: this.schema.id,
      time: this.schema.time,
      indexes: entries.filter(([, d]) => d.index).map(([n]) => n),
      unique: entries.filter(([, d]) => d.unique).map(([n]) => n),
    }
  }
}
