import { describe, expect, test } from 'bun:test';
import { fixedFromEnv } from '../src/connections';

describe('fixedFromEnv', () => {
  test('reads NDB_URL and NDB_TOKEN', () => {
    expect(fixedFromEnv({ NDB_URL: 'tcp://10.0.0.1:7070', NDB_TOKEN: 'envtok' })).toEqual({
      name: 'default',
      url: 'tcp://10.0.0.1:7070',
      token: 'envtok',
    });
  });

  test('takes the token of the URL and keeps it out of the URL', () => {
    expect(fixedFromEnv({ NDB_URL: 'tcp://:a%2Fb@10.0.0.1:7070', NDB_TOKEN: 'envtok' })).toEqual({
      name: 'default',
      url: 'tcp://10.0.0.1:7070',
      token: 'a/b',
    });
  });

  test('an empty password falls back to NDB_TOKEN', () => {
    expect(fixedFromEnv({ NDB_URL: 'tcp://:@10.0.0.1:7070', NDB_TOKEN: 'envtok' })?.token).toBe('envtok');
  });
});
