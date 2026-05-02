// copyToClipboard writes `text` to the system clipboard. Works on both
// secure (HTTPS / localhost) and insecure (plain HTTP) origins.
//
// `navigator.clipboard.writeText` is the modern API but browsers gate it
// to secure contexts only. Synapse self-hosted commonly runs on plain
// HTTP behind an IP (the wizard's "No, just test on this IP" path), so
// we fall back to the legacy `document.execCommand("copy")` flow when
// the modern path is unavailable. execCommand is deprecated but still
// implemented in every browser we care about, and works on HTTP.
//
// Returns `true` on success, `false` on failure (caller surfaces an
// error message).
export async function copyToClipboard(text: string): Promise<boolean> {
  if (
    typeof navigator !== "undefined" &&
    navigator.clipboard?.writeText &&
    typeof window !== "undefined" &&
    window.isSecureContext
  ) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Fall through to the legacy path — some browsers throw even in
      // secure contexts when the page isn't focused or permissions
      // were denied.
    }
  }

  if (typeof document === "undefined") return false;

  const textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.top = "0";
  textarea.style.left = "0";
  textarea.style.width = "1px";
  textarea.style.height = "1px";
  textarea.style.opacity = "0";
  textarea.style.pointerEvents = "none";
  document.body.appendChild(textarea);

  const previousActive = document.activeElement as HTMLElement | null;
  textarea.focus();
  textarea.select();
  textarea.setSelectionRange(0, text.length);

  let ok = false;
  try {
    ok = document.execCommand("copy");
  } catch {
    ok = false;
  } finally {
    document.body.removeChild(textarea);
    previousActive?.focus?.();
  }

  return ok;
}
