import { useEffect, useState, type FormEvent } from "react";
import Brand from "./Brand";

export const waitlistVersion = "2026-11-26-waitlist";
const legalBase = "#/legal/" + waitlistVersion;
export const waitlistHash = "#/waitlist";

async function submit(
  action: "request" | "verify" | "leave",
  payload: Record<string, string>,
) {
  const reply = await fetch("/api/drop/waitlist/" + action, {
    method: "POST",
    redirect: "error",
    credentials: "omit",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
  if (reply.ok) return;
  if (reply.status === 429)
    throw new Error("Too many attempts. Please try again later.");
  if (action === "verify" && [400, 409, 410].includes(reply.status))
    throw new Error(
      "This code is incorrect or has expired. Check it or request a new code.",
    );
  if (action === "leave" && [400, 404, 410].includes(reply.status))
    throw new Error(
      "This link is invalid or has expired. Use the latest link from your waitlist email.",
    );
  throw new Error("Drop could not complete the request. Please try again.");
}

export function isWaitlistLeave(hash: string) {
  return /^#\/waitlist\/leave(?:\?|$)/.test(hash);
}

export default function Waitlist({ hash }: { hash: string }) {
  const leave = isWaitlistLeave(hash);
  const [token] = useState(() => {
    const values = new URLSearchParams(hash.split("?")[1]).getAll("token");
    return values.length === 1 ? values[0] : "";
  });
  useEffect(() => {
    if (leave && location.hash.includes("?"))
      history.replaceState(
        null,
        "",
        location.pathname + location.search + waitlistHash + "/leave",
      );
  }, [leave]);
  const [step, setStep] = useState<"email" | "code" | "done">("email");
  const [address, setAddress] = useState("");
  const [code, setCode] = useState("");
  const [accepted, setAccepted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [status, setStatus] = useState("");

  async function run(action: "request" | "verify" | "leave") {
    if (busy) return;
    setBusy(true);
    setStatus("");
    setError("");
    try {
      if (action === "request") {
        if (!accepted) return;
        await submit(action, {
          email: address.trim(),
          terms_version: waitlistVersion,
        });
        if (step === "code") setStatus("Check your inbox for the latest code.");
        setCode("");
        setStep("code");
      } else {
        await submit(
          action,
          action === "leave" ? { token } : { email: address.trim(), code },
        );
        setStep("done");
      }
    } catch (e) {
      setError(
        e instanceof Error && !(e instanceof TypeError)
          ? e.message
          : "Check your connection and try again.",
      );
    } finally {
      setBusy(false);
    }
  }

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    void run(leave ? "leave" : step === "code" ? "verify" : "request");
  }
  return (
    <main className="sign-in">
      <section className="card waitlist-card">
        <Brand />
        {step === "done" ? (
          <>
            <h1>
              {leave ? "You left the waitlist" : "You’re on the waitlist"}
            </h1>
            <p>
              {leave
                ? "Your waitlist entry has been removed."
                : "If a place opens for you, we’ll send an invitation to your verified email."}
            </p>
          </>
        ) : leave ? (
          <>
            <h1>Leave the waitlist</h1>
            {token ? (
              <form onSubmit={onSubmit}>
                <p>
                  Remove the waitlist entry linked to this email. You can join
                  again later.
                </p>
                <button className="primary" disabled={busy}>
                  Confirm and leave
                </button>
              </form>
            ) : (
              <p role="alert">Open the leave link from your waitlist email.</p>
            )}
          </>
        ) : step === "code" ? (
          <>
            <h1>Verify your email</h1>
            <p>
              Enter the six-digit code sent to <strong>{address.trim()}</strong>{" "}
              to join the waitlist.
            </p>
            <form className="stack" onSubmit={onSubmit}>
              <label>
                Verification code
                <input
                  autoFocus
                  autoComplete="one-time-code"
                  inputMode="numeric"
                  type="text"
                  maxLength={6}
                  value={code}
                  onChange={(e) =>
                    setCode(e.target.value.replace(/\D/g, "").slice(0, 6))
                  }
                  required
                  pattern="[0-9]{6}"
                />
              </label>
              <button className="primary" disabled={busy || code.length !== 6}>
                Verify email
              </button>
            </form>
            <div className="waitlist-actions">
              <button
                className="link"
                disabled={busy}
                onClick={() => void run("request")}
              >
                Resend code
              </button>
              <button
                className="link"
                disabled={busy}
                onClick={() => {
                  setStep("email");
                  setAccepted(false);
                  setCode("");
                  setStatus("");
                  setError("");
                }}
              >
                Change email
              </button>
            </div>
          </>
        ) : (
          <>
            <h1>Join the waitlist</h1>
            <p>Request an invitation to PocketContext Drop Beta.</p>
            <form className="stack" onSubmit={onSubmit}>
              <label>
                Email
                <input
                  required
                  autoFocus
                  type="email"
                  autoComplete="email"
                  maxLength={254}
                  readOnly={busy}
                  value={address}
                  onChange={(e) => setAddress(e.target.value)}
                />
              </label>
              <label className="check">
                <input
                  type="checkbox"
                  required
                  checked={accepted}
                  disabled={busy}
                  onChange={(e) => setAccepted(e.target.checked)}
                />
                <span>
                  I accept the{" "}
                  <a
                    href={legalBase + "/terms"}
                    target="_blank"
                    rel="noreferrer"
                  >
                    Waitlist Terms
                  </a>
                  .
                </span>
              </label>
              <p className="hint">
                The{" "}
                <a
                  href={legalBase + "/privacy"}
                  target="_blank"
                  rel="noreferrer"
                >
                  Waitlist Privacy Notice
                </a>{" "}
                explains how Drop uses your email.
              </p>
              <button className="primary" disabled={busy || !accepted}>
                Send verification code
              </button>
            </form>
          </>
        )}
        {status && <p role="status">{status}</p>}
        {error && (
          <p role="alert" className="error">
            {error}
          </p>
        )}
        <p>
          <a href="#/">Back to Drop</a>
        </p>
      </section>
    </main>
  );
}
