import { useState } from "react";

/** Copies the displayed value and reports whether the clipboard accepted it. */
export default function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
  const [result, setResult] = useState<{ value: string; copied: boolean }>();
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
      <button type="button" className="secondary" onClick={() => void copy()}>
        {current?.copied ? "Copied" : label}
      </button>
      {current && !current.copied && <span role="status">Clipboard unavailable. Select and copy the text manually.</span>}
    </>
  );
}
