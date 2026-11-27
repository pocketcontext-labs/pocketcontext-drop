import { useState } from "react";
import OpenArrow from "./OpenArrow";
import { op } from "./dropApi";
import { message } from "./errors";

interface Ticket { url: string; token: string }

// Drop's own login token never leaves its origin. A fresh runtime credential
// is sent only to the new window after the expected app gateway is ready.
export async function openPrivateApp(app: string, hostname: string): Promise<void> {
  const popup = window.open("about:blank", "_blank");
  if (!popup) throw new Error("Allow a new tab to open this private app.");
  try {
    const ticket = await op<Ticket>("private.access", { app });
    const origin = new URL(ticket.url);
    if (origin.protocol !== "https:" || origin.origin !== ticket.url ||
        origin.origin !== "https://" + hostname || origin.origin === window.location.origin) {
      throw new Error("Drop returned an unexpected private app address.");
    }
    await new Promise<void>((resolve, reject) => {
      const cleanup = () => {
        window.clearTimeout(timeout);
        window.removeEventListener("message", receive);
      };
      const receive = (event: MessageEvent) => {
        if (event.source !== popup || event.origin !== origin.origin || event.data !== "drop-private-ready") return;
        cleanup();
        try {
          popup.postMessage({ type: "drop-private-access", token: ticket.token }, origin.origin);
          resolve();
        } catch (error) { reject(error); }
      };
      const timeout = window.setTimeout(() => {
        cleanup();
        reject(new Error("The private app gateway did not respond. Try again or ask your Drop administrator."));
      }, 15000);
      window.addEventListener("message", receive);
      try { popup.location.replace(origin.origin + "/.well-known/drop/app-session"); }
      catch (error) { cleanup(); reject(error); }
    });
  } catch (error) { popup.close(); throw error; }
}

export function OpenPrivateApp({ app, hostname, disabled = false, primary = false }: { app: string; hostname: string; disabled?: boolean; primary?: boolean }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const open = async () => {
    if (busy) return;
    setBusy(true); setError("");
    try { await openPrivateApp(app, hostname); }
    catch (e) { setError(message(e)); }
    finally { setBusy(false); }
  };
  return <>
    <button type="button" className={primary ? "primary" : "secondary"} disabled={busy || disabled || !hostname} onClick={() => void open()}>
      {busy ? "Opening…" : <>{primary ? "Open app" : "Open"} <OpenArrow /></>}
    </button>
    {error && <p role="alert" className="error">{error}</p>}
  </>;
}
