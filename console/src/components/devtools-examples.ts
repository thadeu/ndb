// The library of examples of Dev Tools. Every body comes from the HTTP
// reference (docs/dev/http/). The reads of a collection use the collection
// of the page. The examples that write, and the reads that need known
// fields, use a collection and a stream of their own, so no example changes
// the data of an app.

export const DEMO_COLLECTION = 'devtools_demo';
export const DEMO_STREAM = 'devtools_jobs';
export const VAULT_COLLECTION = 'devtools_vault';

export interface Example {
  id: string;
  label: string;
  note: string;
  fresh?: boolean;
  text: string;
}

export interface ExampleGroup {
  label: string;
  items: Example[];
}

const lines = (...rows: string[]) => rows.join('\n');

const group = `/${DEMO_STREAM}/_streams/groups/workers`;

export function exampleGroups(collection: string): ExampleGroup[] {
  const read: Example[] = [
    {
      id: 'search',
      label: 'Search with a filter',
      note: 'filter, sort and limit',
      text: lines(
        `POST /${collection}/_search`,
        '{',
        '  "query": { "_id": { "$exists": true } },',
        '  "sort": { "_id": -1 },',
        '  "limit": 10,',
        '  "versions": true',
        '}',
      ),
    },
    {
      id: 'per',
      label: 'Search with per',
      note: 'the first rows of each value; send Bulk insert first',
      text: lines(
        `POST /${DEMO_COLLECTION}/_search`,
        '{',
        '  "sort": { "_id": -1 },',
        '  "per": { "field": "customer_id", "limit": 1 }',
        '}',
      ),
    },
    {
      id: 'unwind',
      label: 'Aggregate with $unwind',
      note: 'one row per array element; send Bulk insert first',
      fresh: true,
      text: lines(
        `POST /${DEMO_COLLECTION}/_aggregate`,
        '{ "pipeline": [',
        '  { "$match": { "status": "paid" } },',
        '  { "$unwind": "$items" },',
        '  { "$group": { "_id": "$items.sku", "qty": { "$sum": "$items.qty" }, "orders": { "$distinct": "$_id" } } },',
        '  { "$sort": { "qty": -1 } },',
        '  { "$limit": 10 }',
        ']}',
      ),
    },
    {
      id: 'count',
      label: 'Count',
      note: 'the number of matches',
      text: lines(`POST /${collection}/_count`, '{ "query": { "_id": { "$exists": true } } }'),
    },
  ];

  const write: Example[] = [
    {
      id: 'bulk',
      label: 'Bulk insert',
      note: `the data of the other examples, in ${DEMO_COLLECTION}`,
      text: lines(
        `POST /${DEMO_COLLECTION}/_bulk`,
        '{',
        '  "docs": [',
        '    { "_id": "a1", "customer_id": "c1", "status": "paid", "balance": 100, "items": [{ "sku": "a", "qty": 2 }, { "sku": "b", "qty": 1 }] },',
        '    { "_id": "a2", "customer_id": "c1", "status": "paid", "balance": 0, "items": [{ "sku": "a", "qty": 5 }] },',
        '    { "_id": "a3", "customer_id": "c2", "status": "new", "balance": 20, "items": [] }',
        '  ],',
        '  "upsert": true',
        '}',
      ),
    },
    {
      id: 'update',
      label: 'Update by query',
      note: 'match and write in one step',
      text: lines(
        `POST /${DEMO_COLLECTION}/_update_by_query`,
        '{',
        '  "query": { "customer_id": "c2", "balance": { "$gte": 1 } },',
        '  "update": { "$inc": { "balance": -1 } }',
        '}',
      ),
    },
    {
      id: 'tx',
      label: 'Transaction (_tx)',
      note: 'one WAL record: all, or none',
      fresh: true,
      text: lines(
        `POST /${DEMO_COLLECTION}/_tx`,
        '{',
        '  "ops": [',
        '    { "op": "update", "id": "a1", "update": { "$inc": { "balance": -50 } } },',
        '    { "op": "update", "id": "a2", "update": { "$inc": { "balance": 50 } } },',
        '    { "op": "insert", "doc": { "kind": "transfer", "from": "a1", "to": "a2", "amount": 50 } }',
        '  ]',
        '}',
      ),
    },
    {
      id: 'lock',
      label: 'Lock and unlock',
      note: 'one holder at a time; the lock ends after ttl',
      fresh: true,
      text: lines(
        `POST /${DEMO_COLLECTION}/_lock/a1`,
        '{ "ttl": "30s", "wait": 0 }',
        '',
        '# put the token of the lock answer here',
        `POST /${DEMO_COLLECTION}/_unlock/a1`,
        '{ "token": "TOKEN" }',
      ),
    },
  ];

  const streams: Example[] = [
    {
      id: 'append',
      label: 'Append and read',
      note: `the first append makes the stream ${DEMO_STREAM}`,
      fresh: true,
      text: lines(
        `POST /${DEMO_STREAM}/_streams/append`,
        '{ "doc": { "order": 42, "total": 99.9 } }',
        '',
        `POST /${DEMO_STREAM}/_streams/read`,
        '{ "after": "0", "count": 10 }',
      ),
    },
    {
      id: 'nack',
      label: 'Nack with a delay',
      note: 'give an entry back to the group for a retry',
      fresh: true,
      text: lines(
        '# a group that exists answers 409 group_exists',
        `PUT ${group}`,
        '{ "start": "0" }',
        '',
        `POST ${group}/read`,
        '{ "consumer": "w1", "count": 1 }',
        '',
        '# put the _id of the read answer here',
        `POST ${group}/nack`,
        '{ "consumer": "w1", "ids": ["0-1"], "delay_ms": 60000 }',
      ),
    },
    {
      id: 'claim',
      label: 'Claim idle entries',
      note: 'take the entries that a consumer left pending',
      fresh: true,
      text: lines(`POST ${group}/claim`, '{ "consumer": "w2", "min_idle_ms": 60000, "cursor": "0-0", "count": 100 }'),
    },
  ];

  const vault = `/${VAULT_COLLECTION}`;

  const encrypted: Example[] = [
    {
      id: 'vault',
      label: 'Declare encrypted fields',
      note: `cpf is blind (found by value), card_token only encrypted, in ${VAULT_COLLECTION}`,
      fresh: true,
      text: lines(`PUT ${vault}`, '{ "blind": { "cpf": "numeric" }, "encrypted": ["card_token"], "unique": ["cpf"] }'),
    },
    {
      id: 'vault-insert',
      label: 'Insert with plain values',
      note: 'the daemon encrypts cpf and card_token before the WAL',
      fresh: true,
      text: lines(
        `POST ${vault}/_bulk`,
        '{',
        '  "docs": [',
        '    { "_id": "u1", "name": "Ana", "cpf": "390.533.447-05", "card_token": "tok_4242" },',
        '    { "_id": "u2", "name": "Bia", "cpf": "111.222.333-44", "card_token": "tok_1881" }',
        '  ],',
        '  "upsert": true',
        '}',
      ),
    },
    {
      id: 'vault-find',
      label: 'Find by a blind field',
      note: 'numeric: only the digits count; the answer stays encrypted',
      fresh: true,
      text: lines(`POST ${vault}/_search`, '{ "query": { "cpf": "39053344705" } }'),
    },
    {
      id: 'vault-decrypt',
      label: 'Read with ?decrypt=1',
      note: 'plain values for a caller with the decrypt right',
      fresh: true,
      text: lines(`GET ${vault}/_doc/u1?decrypt=1`, '', `POST ${vault}/_search?decrypt=1`, '{ "query": { "cpf": { "$in": ["39053344705", "11122233344"] } } }'),
    },
    {
      id: 'vault-group',
      label: 'Group by a blind field',
      note: 'each group gets the value of one of its documents',
      fresh: true,
      text: lines(`POST ${vault}/_aggregate?decrypt=1`, '{ "pipeline": [{ "$group": { "_id": "$cpf", "n": { "$count": {} } } }] }'),
    },
  ];

  const system: Example[] = [
    {
      id: 'health',
      label: 'Health and stats',
      note: 'the state of the daemon',
      text: lines('GET /_health', '', 'GET /_stats'),
    },
    {
      id: 'log',
      label: 'Change the log level',
      note: 'until the daemon stops; ndb.yml keeps the next start',
      fresh: true,
      text: lines('GET /_log', '', 'PUT /_log', '{ "level": "debug" }', '', 'PUT /_log', '{ "level": "info" }'),
    },
    {
      id: 'settings',
      label: 'Settings of the data dir',
      note: 'the encryption key, masked, and the log settings with their source',
      fresh: true,
      text: lines('GET /_cluster/settings'),
    },
    {
      id: 'settings-log',
      label: 'Store the log settings',
      note: 'they hold across a restart; warnings name what ndb.yml, the env or a flag sets',
      fresh: true,
      text: lines('PUT /_cluster/settings', '{ "log": { "level": "debug", "slow_ms": 250 } }', '', 'PUT /_cluster/settings', '{ "log": { "level": "info", "slow_ms": 1000 } }'),
    },
    {
      id: 'index',
      label: 'Create a compound index',
      note: 'built in the background; send Bulk insert first',
      fresh: true,
      text: lines(`PUT /${DEMO_COLLECTION}/_indexes/customer_id,status`, '', `GET /${DEMO_COLLECTION}`),
    },
  ];

  return [
    { label: 'Read', items: read },
    { label: 'Write', items: write },
    { label: 'Streams', items: streams },
    { label: 'Encrypted fields', items: encrypted },
    { label: 'System', items: system },
  ];
}

// findExamples keeps the examples whose label, note or text has the search
// text. A group with no match is left out.
export function findExamples(groups: ExampleGroup[], search: string): ExampleGroup[] {
  const needle = search.trim().toLowerCase();

  if (needle === '') return groups;

  return groups
    .map(item => ({ label: item.label, items: item.items.filter(example => `${example.label}\n${example.note}\n${example.text}`.toLowerCase().includes(needle)) }))
    .filter(item => item.items.length > 0);
}

// placeExample puts an example in the editor text. An example that is
// already there is not added again. It returns the new text and the line of
// the first line of the example.
export function placeExample(text: string, example: string): { text: string; line: number } {
  const at = text.indexOf(example);

  if (at !== -1 && (at === 0 || text[at - 1] === '\n')) return { text, line: text.slice(0, at).split('\n').length - 1 };

  const head = text.replace(/\s+$/, '');
  const base = head === '' ? '' : `${head}\n\n`;

  return { text: `${base}${example}\n`, line: base.split('\n').length - 1 };
}
