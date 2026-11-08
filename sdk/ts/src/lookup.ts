import type { Relation, RelationOptions } from './types.js'

// belongsTo: the foreign key is in this collection (row.fk == other._id).
export function belongsTo(coll: string, fk: string, opts: Pick<RelationOptions, 'pk' | 'fields' | 'lookup'> = {}): Relation {
  return { belongs_to: coll, fk, ...opts }
}

// hasMany: the foreign key is in the other collection (other.fk == row._id).
export function hasMany(coll: string, fk: string, opts: RelationOptions = {}): Relation {
  return { has_many: coll, fk, ...opts }
}

// hasOne is hasMany with one document: an object or null.
export function hasOne(coll: string, fk: string, opts: Omit<RelationOptions, 'limit'> = {}): Relation {
  return { has_one: coll, fk, ...opts }
}
