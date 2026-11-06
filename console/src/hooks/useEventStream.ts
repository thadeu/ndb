import { useEffect, useRef } from 'hono/jsx';

export interface StreamHandlers {
  onMessage: (data: unknown) => void;
  onProblem: (message: string) => void;
  onClose: () => void;
}

// useEventStream opens an SSE stream while url is set, and closes it when url
// becomes undefined or the component goes away. The console route closes the
// upstream watch when the browser disconnects. The stream never reconnects by
// itself: a lost stream is reported to onProblem and the user starts it again.
export function useEventStream(url: string | undefined, handlers: StreamHandlers): void {
  const latest = useRef(handlers);

  latest.current = handlers;

  useEffect(() => {
    if (!url) return;

    const source = new EventSource(url);

    source.onmessage = event => {
      try {
        latest.current.onMessage(JSON.parse(event.data));
      } catch {
        return;
      }
    };

    source.addEventListener('problem', event => {
      latest.current.onProblem((event as MessageEvent).data || 'The stream failed.');
      source.close();
      latest.current.onClose();
    });

    source.addEventListener('end', () => {
      source.close();
      latest.current.onClose();
    });

    source.onerror = () => {
      source.close();
      latest.current.onProblem('The connection to the console was lost.');
      latest.current.onClose();
    };

    return () => source.close();
  }, [url]);
}
