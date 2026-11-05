import type { Readable } from 'node:stream'

export async function* lines(body: Readable): AsyncGenerator<string> {
  let rest = ''

  body.setEncoding('utf8')

  for await (const chunk of body as AsyncIterable<string>) {
    rest += chunk

    let at = rest.indexOf('\n')

    while (at >= 0) {
      const line = rest.slice(0, at)

      rest = rest.slice(at + 1)

      if (line.trim() !== '') {
        yield line
      }

      at = rest.indexOf('\n')
    }
  }

  if (rest.trim() !== '') {
    yield rest
  }
}

export async function* jsonRows(body: Readable): AsyncGenerator<unknown> {
  for await (const line of lines(body)) {
    yield JSON.parse(line)
  }
}
