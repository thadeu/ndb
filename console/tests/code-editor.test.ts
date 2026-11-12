import { describe, expect, it } from 'bun:test';

import { type Stage, isMultiTarget, matchText, moveStage, operatorsOf, suggestOperators } from '@/aggregate';
import { applyCompletion, describeError, editorRows, errorText, formatJson, highlightLine, jsonError, operatorKeyAt } from '@/components/code-editor-model';
import { type ComboOption, customValue, filterOptions, pick, splitList, step } from '@/components/combobox-model';

// at gives the text without the | mark, and the place of the mark.
function at(marked: string): { text: string; caret: number } {
  return { text: marked.replace('|', ''), caret: marked.indexOf('|') };
}

describe('code editor highlight', () => {
  it('colors keys, operators, strings, numbers and literals', () => {
    const tones = (line: string) => highlightLine(line).filter(token => token.text.trim() !== '' && token.tone !== 'plain').map(token => [token.text, token.tone]);

    expect(tones('  "n": { "$sum": "$qty" },')).toEqual([['"n"', 'key'], ['"$sum"', 'op'], ['"$qty"', 'string']]);
    expect(tones('"a": [1, -2.5, true, false, null]')).toEqual([['"a"', 'key'], ['1', 'number'], ['-2.5', 'number'], ['true', 'bool'], ['false', 'bool'], ['null', 'bool']]);
  });

  it('keeps every character of the line', () => {
    const line = '  { "a": "x\\"y", "b": tru }';

    expect(highlightLine(line).map(token => token.text).join('')).toBe(line);
    expect(highlightLine('')).toEqual([]);
  });
});

describe('code editor format and errors', () => {
  it('formats valid JSON only', () => {
    expect(formatJson('{"a":[1,2]}')).toBe('{\n  "a": [\n    1,\n    2\n  ]\n}');
    expect(formatJson('"$items"')).toBe('"$items"');
    expect(formatJson('{"a":')).toBeUndefined();
  });

  it('says nothing for valid JSON and gives a message for a bad one', () => {
    expect(jsonError('{"a": 1}')).toBeUndefined();
    expect(jsonError('10')).toBeUndefined();
    expect(jsonError('  ')?.message).toBe('The text is empty');
    expect(jsonError('{"a": }')?.message).toBeTruthy();
    expect(errorText(jsonError('{"a": }')!)).toStartWith('This is not valid JSON: ');
  });

  it('reads the position of each engine', () => {
    const text = '{\n  "a": }';

    expect(describeError('Unexpected token } in JSON at position 9', text)).toEqual({ message: 'Unexpected token }', line: 2, column: 8 });
    expect(describeError('Unexpected token \'}\', "{\n  "a": }" is not valid JSON', text).line).toBeUndefined();
    expect(describeError('Expected property name or \'}\' in JSON at position 1 (line 1 column 2)', '{x}')).toEqual({ message: "Expected property name or '}'", line: 1, column: 2 });
    expect(describeError('JSON.parse: unexpected character at line 2 column 8 of the JSON data', text)).toEqual({ message: 'unexpected character', line: 2, column: 8 });
    expect(describeError('JSON Parse error: Unexpected token \'}\'', text)).toEqual({ message: "Unexpected token '}'" });

    expect(errorText({ message: 'Unexpected token }', line: 2, column: 8 })).toBe('This is not valid JSON: Unexpected token } (line 2, column 8).');
    expect(errorText({ message: 'Unexpected EOF.' })).toBe('This is not valid JSON: Unexpected EOF.');
  });

  it('grows with the lines between a minimum and a maximum', () => {
    expect(editorRows('{}')).toBe(3);
    expect(editorRows('1\n2\n3\n4\n5')).toBe(5);
    expect(editorRows(Array(40).fill('x').join('\n'))).toBe(16);
  });
});

describe('operator suggestions', () => {
  const names = (op: string, marked: string) => {
    const { text, caret } = at(marked);

    return suggestOperators(op, text, caret)?.items.map(item => item.name);
  };

  it('finds a key that starts with $ and its depth', () => {
    const { text, caret } = at('{ "n": { "$su|" } }');

    expect(operatorKeyAt(text, caret)).toEqual({ from: 10, to: 13, prefix: '$su', depth: 2 });
  });

  it('offers nothing in a value, in an array, or in a string', () => {
    expect(operatorKeyAt(...Object.values(at('{ "_id": "$|" }')) as [string, number])).toBeUndefined();
    expect(operatorKeyAt(...Object.values(at('{ "a": { "$in": ["$|"] } }')) as [string, number])).toBeUndefined();
    expect(operatorKeyAt(...Object.values(at('{ "a": "x \\"$|" }')) as [string, number])).toBeUndefined();
    expect(operatorKeyAt(...Object.values(at('{ "a|" }')) as [string, number])).toBeUndefined();
  });

  it('offers the accumulators in the fields of $group', () => {
    expect(names('$group', '{ "_id": null, "n": { "$|" } }')).toEqual(['$count', '$sum', '$avg', '$min', '$max', '$first', '$last', '$distinct', '$addToSet', '$dateTrunc']);
    expect(names('$group', '{ "_id": null, "n": { "$a|" } }')).toEqual(['$avg', '$addToSet']);
    expect(names('$group', '{ "$|" }')).toBeUndefined();
    expect(names('$group', '{ "_id": "$|" }')).toBeUndefined();
  });

  it('offers the filter operators in $match and $having', () => {
    expect(names('$match', '{ "$|" }')).toEqual(['$and', '$or', '$nor', '$not']);
    expect(names('$match', '{ "qty": { "$g|" } }')).toEqual(['$gt', '$gte']);
    expect(names('$having', '{ "n": { "$e|" } }')).toEqual(['$eq', '$exists', '$elemMatch']);
    expect(names('$match', '{ "$or": [{ "a": 1 }, { "$|" }] }')).toContain('$and');
  });

  it('closes for a full name, an unknown prefix, and a stage with no list', () => {
    expect(names('$match', '{ "qty": { "$gte|" } }')).toBeUndefined();
    expect(names('$match', '{ "qty": { "$zz|" } }')).toBeUndefined();
    expect(names('$sort', '{ "$|" }')).toBeUndefined();
    expect(operatorsOf('$project', 2)).toEqual([]);
  });

  it('writes the picked name, steps over the quote and adds a colon', () => {
    const typed = at('{ "n": { "$su|" } }');
    const edit = applyCompletion(typed.text, operatorKeyAt(typed.text, typed.caret)!, '$sum');

    expect(edit.text).toBe('{ "n": { "$sum": } }');
    expect(edit.text.slice(0, edit.start)).toBe('{ "n": { "$sum": ');

    const keyed = at('{ "n": { "$c|nt": {} } }');

    expect(applyCompletion(keyed.text, operatorKeyAt(keyed.text, keyed.caret)!, '$count').text).toBe('{ "n": { "$count": {} } }');

    const open = at('{ "$o|');

    expect(applyCompletion(open.text, operatorKeyAt(open.text, open.caret)!, '$or').text).toBe('{ "$or": ');
  });
});

describe('stage order and target', () => {
  const stages: Stage[] = [{ op: '$match', body: {} }, { op: '$group', body: { _id: null } }, { op: '$sort', body: { n: -1 } }];
  const ops = (list: Stage[]) => list.map(stage => stage.op);

  it('moves a stage up and down', () => {
    expect(ops(moveStage(stages, 2, -1))).toEqual(['$match', '$sort', '$group']);
    expect(ops(moveStage(stages, 0, 1))).toEqual(['$group', '$match', '$sort']);
    expect(ops(stages)).toEqual(['$match', '$group', '$sort']);
  });

  it('does not move a stage over an end', () => {
    expect(moveStage(stages, 0, -1)).toBe(stages);
    expect(moveStage(stages, 2, 1)).toBe(stages);
    expect(moveStage(stages, 7, 1)).toBe(stages);
  });

  it('knows a list and a pattern, and says how many collections match', () => {
    expect(isMultiTarget('orders')).toBe(false);
    expect(isMultiTarget('orders-2026-*')).toBe(true);
    expect(isMultiTarget('a,b')).toBe(true);

    expect(matchText(0)).toBe('No collection matches this target.');
    expect(matchText(1)).toBe('1 collection matches this target.');
    expect(matchText(3)).toBe('3 collections match this target.');
  });
});

describe('combobox', () => {
  const options: ComboOption[] = [{ id: 'customers' }, { id: 'orders' }, { id: 'orders-2026-11' }, { id: 'reorders' }];
  const ids = (list: ComboOption[]) => list.map(option => option.id);

  it('filters without case, with the names that start with the text first', () => {
    expect(ids(filterOptions(options, ''))).toEqual(['customers', 'orders', 'orders-2026-11', 'reorders']);
    expect(ids(filterOptions(options, 'ORD'))).toEqual(['orders', 'orders-2026-11', 'reorders']);
    expect(ids(filterOptions(options, 'tom'))).toEqual(['customers']);
    expect(filterOptions(options, 'zzz')).toEqual([]);
  });

  it('offers the typed text when it is not an option', () => {
    expect(customValue(options, 'orders-2026-*')).toBe('orders-2026-*');
    expect(customValue(options, ' new_one ')).toBe('new_one');
    expect(customValue(options, 'orders,customers')).toBe('orders,customers');
    expect(customValue(options, 'orders')).toBeUndefined();
    expect(customValue(options, '  ')).toBeUndefined();
  });

  it('works on the last item of a comma list', () => {
    expect(splitList('orders,cu', true)).toEqual({ head: 'orders,', part: 'cu' });
    expect(splitList('orders,cu', false)).toEqual({ head: '', part: 'orders,cu' });
    expect(pick('orders,cu', 'customers', true)).toBe('orders,customers');
    expect(pick('ord', 'orders', true)).toBe('orders');
    expect(pick('a,b', 'orders', false)).toBe('orders');
  });

  it('moves the active row around the list', () => {
    expect(step(-1, 3, 1)).toBe(0);
    expect(step(-1, 3, -1)).toBe(2);
    expect(step(2, 3, 1)).toBe(0);
    expect(step(0, 3, -1)).toBe(2);
    expect(step(-1, 0, 1)).toBe(-1);
  });
});
