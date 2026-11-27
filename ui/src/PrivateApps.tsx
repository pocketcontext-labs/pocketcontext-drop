import { useEffect, useState, type FormEvent } from "react";
import { op } from "./dropApi";
import { message } from "./errors";

interface SharedApp { id: string; name: string; hostname: string }
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

export function OpenPrivateApp({ app, hostname, disabled = false }: { app: string; hostname: string; disabled?: boolean }) {
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
    <button type="button" className="secondary" disabled={busy || disabled || !hostname} onClick={() => void open()}>
      {busy ? "Opening…" : "Open private app"}
    </button>
    {error && <p role="alert" className="error">{error}</p>}
  </>;
}

export function PrivateTesters({ app }: { app: string }) {
  const [email, setEmail] = useState("");
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");
  const [error, setError] = useState("");
  const change = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (busy) return;
    const submitter = (event.nativeEvent as SubmitEvent).submitter as HTMLButtonElement | null;
    const revoke = submitter?.value === "revoke";
    const recipient = email.trim();
    setBusy(true); setError(""); setStatus("");
    try {
      await op(revoke ? "private.revoke" : "private.grant", { app, email: recipient });
      setStatus(revoke ? `Removed access for ${recipient}.` : `${recipient} can now open this app in Drop.`);
      setEmail("");
    } catch (e) { setError(message(e)); }
    finally { setBusy(false); }
  };
  return <section aria-labelledby="testers-title">
    <h3 id="testers-title">Testers</h3>
    <p>Give someone access to run this app using their existing Drop account. Its releases stay private to you.</p>
    <form onSubmit={change}>
      <label>Tester email<input type="email" required value={email} onChange={e => setEmail(e.target.value)} /></label>
      <div className="row">
        <button type="submit" value="grant" className="secondary" disabled={busy}>Grant access</button>
        <button type="submit" value="revoke" className="secondary" disabled={busy}>Remove access</button>
      </div>
    </form>
    {error && <p role="alert" className="error">{error}</p>}
    {status && <p role="status">{status}</p>}
  </section>;
}

export function SharedApps() {
  const [apps, setApps] = useState<SharedApp[]>([]);
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    const load = () => void op<{ apps: SharedApp[] }>("private.shared").then(result => {
      if (active) { setApps(result.apps); setError(""); }
    }).catch(e => { if (active) setError(message(e)); });
    load();
    window.addEventListener("focus", load);
    return () => { active = false; window.removeEventListener("focus", load); };
  }, []);
  if (!error && !apps.length) return null;
  return <section className="panel wide" aria-labelledby="shared-apps-title">
    <h2 id="shared-apps-title">Shared with you</h2>
    {error && <p role="alert" className="error">{error}</p>}
    <ul className="apps">{apps.map(app => <li className="row" key={app.id}>
      <span>{app.name}</span><OpenPrivateApp app={app.id} hostname={app.hostname} />
    </li>)}</ul>
  </section>;
}
