import type { Redis } from 'ioredis'
import type { RedisClientType } from 'redis'
import { expectTypeOf, it } from 'vitest'
import { connect } from '../src/index.js'
import { ioredis, type IoredisStreams } from '../src/ioredis.js'
import { nodeRedis, type NodeRedisStreams } from '../src/node-redis.js'

const db = connect('tcp://127.0.0.1:1')

type IoredisCommands = Pick<Redis, keyof IoredisStreams>

// The two answers that the types of node-redis 5 do not give as Redis 7
// does: they have their own test below.
type Loose = 'xInfoStream' | 'xInfoGroups'
type NodeRedisCommands = Pick<RedisClientType, Exclude<keyof NodeRedisStreams, Loose>>

type Reply<K extends Loose> = Awaited<ReturnType<RedisClientType[K]>>
type Ours<K extends Loose> = Awaited<ReturnType<NodeRedisStreams[K]>>

// The fields of Redis 8.6 (idempotent appends), which Redis 7 does not give.
type Idempotence = 'idmp-duration' | 'idmp-maxsize' | 'pids-tracked' | 'iids-tracked' | 'iids-added' | 'iids-duplicates'

// node-redis types these two as a number. Redis gives an ID (a string), and
// null for a lag that it cannot know.
type Mistyped = 'last-delivered-id' | 'lag'

it('ioredis(db) is assignable to the stream commands of an ioredis client', () => {
  const streams: IoredisCommands = ioredis(db)

  void streams
})

it('nodeRedis(db) is assignable to the stream commands of a node-redis client', () => {
  const streams: NodeRedisCommands = nodeRedis(db)

  void streams
})

it('xInfoStream has the parameters and the Redis 7 fields of node-redis', () => {
  expectTypeOf<Parameters<NodeRedisStreams['xInfoStream']>>().toEqualTypeOf<Parameters<RedisClientType['xInfoStream']>>()
  expectTypeOf<Ours<'xInfoStream'>>().toExtend<Omit<Reply<'xInfoStream'>, Idempotence>>()
  expectTypeOf<keyof Ours<'xInfoStream'>>().toEqualTypeOf<Exclude<keyof Reply<'xInfoStream'>, Idempotence>>()
})

it('xInfoGroups has the parameters and the fields of node-redis', () => {
  expectTypeOf<Parameters<NodeRedisStreams['xInfoGroups']>>().toEqualTypeOf<Parameters<RedisClientType['xInfoGroups']>>()
  expectTypeOf<Omit<Ours<'xInfoGroups'>[number], Mistyped>>().toExtend<Omit<Reply<'xInfoGroups'>[number], Mistyped>>()
  expectTypeOf<keyof Ours<'xInfoGroups'>[number]>().toEqualTypeOf<keyof Reply<'xInfoGroups'>[number]>()
})

it('rejects a call that the library rejects', () => {
  // @ts-expect-error a message is an object, not a list of fields
  void nodeRedis(db).xAdd('orders', '*', ['order_id', '42'])

  // @ts-expect-error node-redis takes the count in the options
  void nodeRedis(db).xRange('orders', '-', '+', 10)
})
