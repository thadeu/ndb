import { describe, expect, it } from 'bun:test';

import { buildWhere, cellText, chipsOf, columnsOf, findParams, readFind, searchBody, withoutRow } from '@/components/filter-model';

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

  it('reads per from the query string and keeps the default limit out of it', () => {
    const query = readFind(new URLSearchParams('per=customer_id&per_limit=3'));

    expect(query.per).toBe('customer_id');
    expect(query.perLimit).toBe(3);
    expect(findParams(query).toString()).toBe('per=customer_id&per_limit=3');

    const plain = readFind(new URLSearchParams('per=customer_id&per_limit=x'));

    expect(plain.perLimit).toBe(5);
    expect(findParams(plain).toString()).toBe('per=customer_id');
    expect(readFind(new URLSearchParams('per_limit=9')).per).toBe('');
  });

  it('sends per in place of limit', () => {
    const query = readFind(new URLSearchParams('sort=ts&per=customer_id&per_limit=2&limit=50'));

    expect(searchBody(query, {})).toEqual({ query: {}, sort: { ts: -1 }, per: { field: 'customer_id', limit: 2 } });
  });

  it('labels each active filter as a chip', () => {
    const chips = chipsOf([
      { field: 'status', op: 'in', type: 'string', value: 'paid,new' },
      { field: '', op: 'eq', type: 'string', value: 'skipped' },
      { field: 'qty', op: 'gte', type: 'number', value: '2' },
      { field: 'gone', op: 'missing', type: 'string', value: 'ignored' },
      { field: 'note', op: 'eq', type: 'null', value: '' },
    ]);

    expect(chips.map(chip => chip.label)).toEqual(['status in paid, new', 'qty ≥ 2', 'gone does not exist', 'note = null']);
    expect(chips.map(chip => chip.index)).toEqual([0, 2, 3, 4]);
    expect(chips[1]).toMatchObject({ field: 'qty', op: '≥', value: '2' });
  });

  it('removes one filter and keeps the rest of the query', () => {
    const query = readFind(new URLSearchParams('f=status&op=eq&t=string&v=paid&f=qty&op=gte&t=number&v=2&sort=ts&per=sku'));

    expect(findParams(withoutRow(query, 0)).toString()).toBe('f=qty&op=gte&t=number&v=2&sort=ts&per=sku');
    expect(findParams(withoutRow(query, 1)).toString()).toBe('f=status&op=eq&t=string&v=paid&sort=ts&per=sku');
    expect(query.rows).toHaveLength(2);
  });

  it('picks columns and short cells', () => {
    expect(columnsOf([{ _id: 1, _v: 1, b: 1 }, { _id: 2, a: 1 }])).toEqual(['_id', 'b', 'a']);
    expect(cellText({ x: 1 })).toBe('{"x":1}');
    expect(cellText('abcdef', 4)).toBe('abc…');
  });
});
