/**
 * Copy to the clipboard. `navigator.clipboard` only exists in secure contexts (HTTPS or localhost).
 * When the platform is deployed in `--http` mode (e.g. a quick intranet-only test) it is `http://<ip>`, not a secure context, so `navigator.clipboard`
 * is `undefined` and calling `.writeText` throws. Prefer the Clipboard API, and when it is unavailable or fails
 * fall back to the old `document.execCommand('copy')` (hidden textarea + select), so copying works in both contexts.
 */
export async function copyToClipboard(text: string): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // fall through to the fallback
    }
  }
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  // Insert next to the triggering element rather than document.body: dialogs (Radix Dialog) have a focus trap and body is outside it,
  // so focusing the textarea gets pulled back into the dialog, the selection is lost and execCommand silently fails to copy.
  const anchor = document.activeElement;
  const host = anchor instanceof HTMLElement && anchor.parentElement ? anchor.parentElement : document.body;
  host.appendChild(ta);
  ta.focus();
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch {
    ok = false;
  }
  host.removeChild(ta);
  return ok;
}
