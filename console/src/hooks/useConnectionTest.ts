export interface TestResult {
  ok: boolean;
  message: string;
}

// useConnectionTest posts a form to the Test route and returns the answer.
// It never throws: a failed request becomes a not-ok result.
export function useConnectionTest(url: string) {
  return async (form: HTMLFormElement): Promise<TestResult> => {
    try {
      const response = await fetch(url, { method: 'POST', body: new FormData(form), headers: { accept: 'application/json' } });

      return (await response.json()) as TestResult;
    } catch {
      return { ok: false, message: 'The console did not answer.' };
    }
  };
}
