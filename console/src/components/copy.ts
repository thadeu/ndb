// copyText puts text on the clipboard. It returns false when the browser
// refuses, such as on a page that is not secure.
export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);

    return true;
  } catch {
    return false;
  }
}
