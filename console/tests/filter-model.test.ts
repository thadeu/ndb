import { describe, expect, it } from 'bun:test';

import { buildWhere, cellText, columnsOf, findParams, readFind, searchBody } from '@/components/filter-model';

describe('filter model', () => {
  it('builds equality, typed values and joined ranges', () => {
    const { where, error } = buildWhere([
      { field: 'status', op: 'eq', type: 'string', value: 'paid' },
      { field: 'total', op: 'gte', type: 'number', value: '10' },
      { field: 'total', op: 'lt', type: 'number', value: '99' },
      { field: 'vip', op: 'eq', type: 'boolean', value: 'true' },
      { field: '', op: 'eq', type: 'string', value: 'skipped' },
    ]);

    expect(error).toBeUndefined();
    expect(where).toEqual({ status: 'paid', total: { $gte: 10, $lt: 99 }, vip: true });
  });

  it('builds lists, prefix, regex and exists', () => {
    const { where } = buildWhere([
      { field: 'uf', op: 'in', type: 'string', value: 'SP, RJ' },
      { field: 'name', op: 'prefix', type: 'string', value: 'a.b' },
      { field: 'city', op: 'regex', type: 'string', value: '^S' },
      { field: 'gone', op: 'missing', type: 'string', value: '' },
    ]);

    expect(where).toEqual({ uf: { $in: ['SP', 'RJ'] }, name: { $regex: '^a\\.b' }, city: { $regex: '^S' }, gone: { $exists: false } });
  });

  it('reports a bad number', () => {
    expect(buildWhere([{ field: 'n', op: 'eq', type: 'number', value: 'x' }]).error).toContain('not a number');
  });

  it('reads and writes the query string', () => {
    const query = readFind(new URLSearchParams('f=status&op=eq&t=string&v=paid&sort=ts&dir=asc&limit=50'));

    expect(query.rows).toEqual([{ field: 'status', op: 'eq', type: 'string', value: 'paid' }]);
    expect(findParams(query).toString()).toBe('f=status&op=eq&t=string&v=paid&sort=ts&dir=asc&limit=50');
    expect(readFind(new URLSearchParams('')).rows).toHaveLength(1);
  });

  it('makes the search body with sort and fields', () => {
    const query = readFind(new URLSearchParams('sort=ts&fields=a,%20b'));

    expect(searchBody(query, { a: 1 })).toEqual({ query: { a: 1 }, sort: { ts: -1 }, fields: ['_id', 'a', 'b'], limit: 25 });
  });

  it('picks columns and short cells', () => {
    expect(columnsOf([{ _id: 1, _v: 1, b: 1 }, { _id: 2, a: 1 }])).toEqual(['_id', 'b', 'a']);
    expect(cellText({ x: 1 })).toBe('{"x":1}');
    expect(cellText('abcdef', 4)).toBe('abc…');
  });
});
