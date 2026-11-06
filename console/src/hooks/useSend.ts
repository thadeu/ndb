export interface SendResult<T> {
  ok: boolean;
  status: number;
  body: T;
}

// useSend posts JSON to a console route and returns the answer. It never
// throws: a failed request becomes a result with status 0.
export function useSend(url: string) {
  return async <T = Record<string, unknown>>(payload: unknown): Promise<SendResult<T>> => {
    try {
      const response = await fetch(url, {
        method: 'POST',
        headers: { 'content-type': 'application/json', accept: 'application/json' },
        body: JSON.stringify(payload),
      });
      const body = (await response.json().catch(() => ({}))) as T;

      return { ok: response.ok, status: response.status, body };
    } catch {
      return { ok: false, status: 0, body: { message: 'The console did not answer.' } as T };
    }
  };
}
