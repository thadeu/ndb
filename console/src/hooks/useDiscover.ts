export interface Found {
  url: string;
  state: 'ok' | 'starting' | 'needs_token';
  version?: string;
  collections?: number;
}

export interface DiscoverView {
  phase: 'idle' | 'searching' | 'done' | 'error';
  results: Found[];
  hint?: string;
  message?: string;
}

// useDiscover posts the host and ports to the Discover route and tells each
// change of state to onChange. It never throws.
export function useDiscover(url: string, onChange: (view: DiscoverView) => void) {
  let view: DiscoverView = { phase: 'idle', results: [] };

  const set = (next: DiscoverView) => {
    view = next;
    onChange(view);
  };

  return {
    get view() {
      return view;
    },

    async run(host: string, ports: string): Promise<void> {
      set({ phase: 'searching', results: [] });

      try {
        const response = await fetch(url, { method: 'POST', body: JSON.stringify({ host, ports }), headers: { 'content-type': 'application/json', accept: 'application/json' } });
        const body = (await response.json()) as { found?: Found[]; hint?: string; error?: string };

        if (!response.ok) return set({ phase: 'error', results: [], message: body.error ?? 'Discover failed.' });

        set({ phase: 'done', results: body.found ?? [], hint: body.hint });
      } catch {
        set({ phase: 'error', results: [], message: 'The console did not answer.' });
      }
    },
  };
}
