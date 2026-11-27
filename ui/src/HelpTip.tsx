import { useId, useState } from "react";

export default function HelpTip({ topic, children }: { topic: string; children: string }) {
  const id = useId();
  const [open, setOpen] = useState(false);
  return <>
    <button type="button" className="help-tip" aria-label={`Help with ${topic}`}
      aria-expanded={open} aria-controls={id} onClick={() => setOpen(!open)}
      onKeyDown={event => { if (event.key === "Escape") setOpen(false); }}>
      <span aria-hidden="true">?</span>
    </button>
    <span id={id} className="help-answer" hidden={!open}>{children}</span>
  </>;
}
