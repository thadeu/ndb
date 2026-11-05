import type { Model } from './model.js'
import type { Fields, Row, SortSpec, Where } from './types.js'
import { toWhere } from './where.js'

interface State {
  clauses: Record<string, unknown>[]
  fields?: string[]
  sort?: Record<string, 1 | -1>
  limit?: number
  skip?: number
  after?: string
}

export class Query<F extends Fields, R = Row<F>> implements PromiseLike<R[]> {
  nextCursor = ''

  constructor(
    private readonly model: Model<F>,
    private readonly state: State = { clauses: [] },
  ) {}

  where(w: Where<F>): Query<F, R> {
    return this.with({ clauses: [...this.state.clauses, toWhere(w) as Record<string, unknown>] })
  }

  not(w: Where<F>): Query<F, R> {
    return this.with({ clauses: [...this.state.clauses, { $not: toWhere(w) }] })
  }

  or(...ws: Where<F>[]): Query<F, R> {
    return this.with({ clauses: [...this.state.clauses, { $or: ws.map(toWhere) }] })
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

  async count(): Promise<number> {
    await this.model.ready()

    const body = { where: this.whereDoc(), count: true, limit: 1 }
    const res = await this.model.db.transport.stream('POST', this.model.path('find'), body)

    await res.body.dump()

    return Number(res.header('x-ndb-total') ?? 0)
  }

  async *iterate(): AsyncGenerator<R> {
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
    await this.model.ready()

    const res = await this.model.db.transport.stream('POST', this.model.path('find'), body)

    this.nextCursor = res.header('x-ndb-next') ?? ''

    for await (const raw of res.rows()) {
      yield this.model.decode(raw as Record<string, unknown>) as R
    }
  }

  private body(): Record<string, unknown> {
    const { fields, sort, limit, skip, after } = this.state

    return { where: this.whereDoc(), fields, sort, limit, skip, after, versions: true }
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
    return new Query<F, R>(this.model, { ...this.state, ...patch })
  }
}
