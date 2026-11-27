import { useEffect, useState } from "react";

/** Copies the displayed value and reports whether the clipboard accepted it. */
export default function CopyButton({ text, label = "Copy", icon = false, showAddress = false }: { text: string; label?: string; icon?: boolean; showAddress?: boolean }) {
  const [result, setResult] = useState<{ value: string; copied: boolean }>();
  useEffect(() => {
    if (!result?.copied) return;
    const timer = window.setTimeout(() => setResult(undefined), 2000);
    return () => window.clearTimeout(timer);
  }, [result]);
  async function copy() {
    let copied = false;
    try {
      await navigator.clipboard.writeText(text);
      copied = true;
    } catch {
      // The browser can deny clipboard permission or lack the Clipboard API.
    }
    setResult({ value: text, copied });
  }
  const current = result?.value === text ? result : undefined;
  return (
    <>
      <button type="button" className={showAddress ? "secondary connect-address" : "secondary"} aria-label={current?.copied ? "Copied" : label} title={current?.copied ? "Copied" : label} onClick={() => void copy()}>
        {showAddress && <code>{text}</code>}
        {icon || showAddress ? <svg aria-hidden="true" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
          {current?.copied ? <path d="m4 12 5 5L20 6" /> : <><rect x="8" y="8" width="12" height="13" rx="2" /><path d="M15 5V3H3v13h2" /></>}
        </svg> : current?.copied ? "Copied" : label}
      </button>
      {current && !current.copied && <span role="status">Clipboard unavailable. Select and copy the text manually.</span>}
    </>
  );
}
