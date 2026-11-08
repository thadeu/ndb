import type { Collection } from './collection.js'
import type { Fields, Relation, Row, SortSpec, Where } from './types.js'
import { toWhere } from './where.js'

interface State {
  clauses: Record<string, unknown>[]
  fields?: string[]
  sort?: Record<string, 1 | -1>
  limit?: number
  skip?: number
  after?: string
  lookup?: Record<string, Relation>
}

export class Query<F extends Fields, R = Row<F>> implements PromiseLike<R[]> {
  nextCursor = ''

  constructor(
    private readonly coll: Collection<F>,
    private readonly state: State = { clauses: [] },
  ) {}

  // Adds conditions, ANDed with the ones already there.
  query(w: Where<F>): Query<F, R> {
    return this.with({ clauses: [...this.state.clauses, toWhere(w) as Record<string, unknown>] })
  }

  // A query built elsewhere, such as from a search form. It is not
  // type-checked and is sent as is; the daemon validates it. It is ANDed
  // with the other clauses.
  rawQuery(w: Record<string, unknown>): Query<F, R> {
    return this.with({ clauses: [...this.state.clauses, w] })
  }

  not(w: Where<F>): Query<F, R> {
    return this.with({ clauses: [...this.state.clauses, { $not: toWhere(w) }] })
  }

  or(...ws: Where<F>[]): Query<F, R> {
    return this.with({ clauses: [...this.state.clauses, { $or: ws.map(toWhere) }] })
  }

  // Adds a relation: each row gets the related documents under name (POST
  // /{coll}/_search with "lookup"). T types what it holds.
  lookup<K extends string, T = Record<string, unknown>>(name: K, rel: Relation): Query<F, R & { [P in K]: T }> {
    return new Query<F, R & { [P in K]: T }>(this.coll, { ...this.state, lookup: { ...this.state.lookup, [name]: rel } })
  }

  sort(spec: SortSpec<F>): Query<F, R> {
    return this.with({ sort: { ...this.state.sort, ...(spec as Record<string, 1 | -1>) } })
  }

  limit(n: number): Query<F, R> {
    return this.with({ limit: n })
  }

  skip(n: number): Query<F, R> {
    return this.with({ skip: n })
  }

  page(n: number, size: number): Query<F, R> {
    return this.with({ skip: (n - 1) * size, limit: size })
  }

  after(token: string): Query<F, R> {
    return this.with({ after: token })
  }

  fields(...names: string[]): Query<F, Partial<R>> {
    return this.with({ fields: names }) as unknown as Query<F, Partial<R>>
  }

  then<A = R[], B = never>(
    onfulfilled?: ((value: R[]) => A | PromiseLike<A>) | null,
    onrejected?: ((reason: unknown) => B | PromiseLike<B>) | null,
  ): Promise<A | B> {
    return this.all().then(onfulfilled, onrejected)
  }

  async all(): Promise<R[]> {
    const out: R[] = []

    for await (const row of this.rows(this.body())) {
      out.push(row)
    }

    return out
  }

  async first(): Promise<R | null> {
    for await (const row of this.rows({ ...this.body(), limit: 1 })) {
      return row
    }

    return null
  }

  // How many documents match (POST /{coll}/_count). limit, skip and sort do
  // not change it.
  async count(): Promise<number> {
    await this.coll.ready()

    const out = await this.coll.db.transport.request<{ count: number }>('POST', this.coll.path('_count'), { query: this.whereDoc() })

    return out.count
  }

  // Every row, following the cursor page by page (limit is the page size).
  async *each(): AsyncGenerator<R> {
    let body = this.body()

    for (;;) {
      yield* this.rows(body)

      if (this.nextCursor === '') {
        return
      }

      body = { ...body, after: this.nextCursor, skip: undefined }
    }
  }

  private async *rows(body: Record<string, unknown>): AsyncGenerator<R> {
    await this.coll.ready()

    const res = await this.coll.db.transport.stream('POST', this.coll.path('_search'), body)

    this.nextCursor = res.header('x-ndb-next') ?? ''

    for await (const raw of res.rows()) {
      yield this.coll.decode(raw as Record<string, unknown>) as R
    }
  }

  private body(): Record<string, unknown> {
    const { fields, sort, limit, skip, after, lookup } = this.state

    return { query: this.whereDoc(), fields, sort, limit, skip, after, lookup, versions: true }
  }

  private whereDoc(): Record<string, unknown> {
    const merged: Record<string, unknown> = {}

    for (const clause of this.state.clauses) {
      for (const key of Object.keys(clause)) {
        if (key in merged) {
          return { $and: this.state.clauses }
        }

        merged[key] = clause[key]
      }
    }

    return merged
  }

  private with(patch: Partial<State>): Query<F, R> {
    return new Query<F, R>(this.coll, { ...this.state, ...patch })
  }
}
