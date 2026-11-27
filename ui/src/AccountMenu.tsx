import { useEffect, useRef, useState } from "react";
import { api } from "./api";

export default function AccountMenu({ email }: { email: string }) {
  const [open, setOpen] = useState(false);
  const container = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const words = email.split("@")[0].split("+")[0].match(/[\p{L}\p{N}]+/gu) || [];
  const initials = (words.length > 1 ? words.slice(0, 1).concat(words.slice(-1)).map(word => word.charAt(0)).join("") : words[0]?.slice(0, 2) || "ME").toLocaleUpperCase();
  useEffect(() => {
    if (!open) return;
    function dismiss(event: PointerEvent) {
      if (!container.current?.contains(event.target as Node)) setOpen(false);
    }
    document.addEventListener("pointerdown", dismiss);
    return () => document.removeEventListener("pointerdown", dismiss);
  }, [open]);
  return <div className="account-menu" ref={container}
    onBlur={event => { if (!event.currentTarget.contains(event.relatedTarget)) setOpen(false); }}
    onKeyDown={event => { if (event.key === "Escape") { setOpen(false); trigger.current?.focus(); } }}>
    <button type="button" className="account-avatar" aria-label="Account menu" aria-expanded={open}
      ref={trigger} onClick={() => setOpen(!open)}>{initials}</button>
    {open && <div className="account-dropdown">
      <span>{email}</span>
      <button type="button" className="secondary" onClick={() => api.logout()}>Sign out</button>
    </div>}
  </div>;
}
