import { useState } from "react";

/** Copies a text to the clipboard and says so. */
export default function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className="secondary"
      onClick={() =>
        void navigator.clipboard?.writeText(text).then(
          () => setCopied(true),
          () => setCopied(false),
        )
      }
    >
      {copied ? "Copied" : "Copy"}
    </button>
  );
}
