import { useRef, useState, type ClipboardEvent, type FormEvent } from "react";
import { api, type PublishedLegal } from "./api";
import Brand from "./Brand";
import { BetaLegalLinks } from "./BetaLegal";
import { message } from "./errors";
import {
  clearLoginReturn,
  loginReturnHash,
  rememberLoginReturn,
  type EmailLoginLink,
} from "./routes";

/** An installed home-screen app keeps its own cookies and storage, apart from the browser. */
export function isStandaloneApp(): boolean {
  try {
    return (
      window.matchMedia?.("(display-mode: standalone)")?.matches === true ||
      (navigator as Navigator & { standalone?: boolean }).standalone === true
    );
  } catch {
    return false;
  }
}
const codeLength = 6;
const codeDigits = (value: string) =>
  value.replace(/\D/g, "").slice(0, codeLength);

/**
 * Hosted sign-in by email (ported from Crew): the address, then the six-digit
 * code from the email, or the email's link (`#/login?…`), which asks to
 * confirm so a mail scanner that opens links cannot spend it.
 */
export default function Login({
  emailLink,
  onComplete,
  initialEmail = "",
  betaEnrollment = false,
  documents,
}: {
  emailLink?: EmailLoginLink;
  onComplete?: () => void;
  /** The address of an invite's sign-in URL. */
  initialEmail?: string;
  betaEnrollment?: boolean;
  documents?: PublishedLegal;
}) {
  const [accepted, setAccepted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [sentTo, setSentTo] = useState("");
  const [address, setAddress] = useState(initialEmail);
  const [otpId, setOtpId] = useState("");
  const [code, setCode] = useState("");
  const redeeming = useRef(false);
  const [link, setLink] = useState(emailLink);
  const [linkFailed, setLinkFailed] = useState(false);
  const run = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await action();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const email = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (betaEnrollment && documents && !accepted) return;
    const form = new FormData(event.currentTarget);
    const value = String(form.get("email"));
    const hash = location.hash;
    setAddress(value);
    void run(async () => {
      const id = await api.requestEmailLogin(value);
      const target = loginReturnHash(hash);
      if (target) rememberLoginReturn(target);
      // Asking again from a spent link's #/login view keeps the original target.
      else if (!hash.startsWith("#/login")) clearLoginReturn();
      setOtpId(id);
      setCode("");
      setSentTo(value);
    });
  };
  const redeem = (value: string) => {
    if (redeeming.current) return;
    if (value.length !== codeLength) {
      setError("Enter the 6-digit code from the email.");
      return;
    }
    redeeming.current = true;
    void run(async () => {
      try {
        if (accepted && documents)
          await api.loginEmailCode(otpId, value, documents.termsVersion);
        else await api.loginEmailCode(otpId, value);
      } catch (e) {
        setCode("");
        throw e;
      } finally {
        redeeming.current = false;
      }
      // The link flow returns to the stored target; here the address never left it.
      if (onComplete) onComplete();
      else clearLoginReturn();
    });
  };
  const enterCode = (value: string) => {
    const digits = codeDigits(value);
    setCode(digits);
    if (digits.length === codeLength) redeem(digits);
  };
  const pasteCode = (event: ClipboardEvent<HTMLInputElement>) => {
    // maxLength would cut "123 456" before the spaces are stripped.
    event.preventDefault();
    enterCode(event.clipboardData.getData("text"));
  };
  const newCode = () => {
    setAccepted(false);
    setSentTo("");
    setOtpId("");
    setCode("");
    setError("");
  };
  const confirm = () =>
    void run(async () => {
      if (!link) return;
      try {
        if (accepted && documents)
          await api.loginEmail(link.otpId, link.password, documents.termsVersion);
        else await api.loginEmail(link.otpId, link.password);
      } catch (error) {
        const code = (error as { response?: { code?: string } })?.response
          ?.code;
        if (code === "terms_required" || code === "beta_activation_closed") {
          setLink(undefined);
          setError(
            code === "terms_required"
              ? "Account activation requires the current Beta terms. Review them and request a new code."
              : "New Beta activation is paused. Existing accounts can still sign in.",
          );
          return;
        }
        setLinkFailed(true);
        return;
      }
      setLink(undefined);
      onComplete?.();
    });
  const legal = documents ? (
    <>
      <label className="check">
        <input
          type="checkbox"
          checked={accepted}
          onChange={(event) => setAccepted(event.target.checked)}
        />
        <span>
          I accept the{" "}
          <a href={documents.termsURL} target="_blank" rel="noreferrer">
            Beta Terms of Service
          </a>{" "}
          for account activation.
        </span>
      </label>
      <p className="hint">
        The{" "}
        <a href={documents.privacyURL} target="_blank" rel="noreferrer">
          Privacy Policy
        </a>{" "}
        explains how Drop uses your information.
      </p>
    </>
  ) : betaEnrollment ? (
    <p className="hint">
      New Beta activation is paused. Existing accounts can still sign in.
    </p>
  ) : null;
  let body;
  if (link) {
    body =
      link.otpId && !linkFailed ? (
        <>
          <h1 id="sign-in-title">Log in to Drop</h1>
          {legal}
          <button
            className="primary"
            autoFocus
            disabled={busy}
            onClick={confirm}
          >
            Continue to Drop
          </button>
        </>
      ) : (
        <>
          <h1 id="sign-in-title">This link no longer works</h1>
          <button
            className="primary"
            autoFocus
            onClick={() => {
              setLink(undefined);
              setLinkFailed(false);
            }}
          >
            Send a new link
          </button>
        </>
      );
  } else if (sentTo) {
    body = (
      <>
        <h1 id="sign-in-title">Check your inbox</h1>
        <p role="status">
          If {sentTo} has a Drop account or an invitation, a sign-in code and
          link are on their way. They work for 15 minutes.
        </p>
        <p className="hint">
          {isStandaloneApp()
            ? "Enter the code from the email here. The link in the email opens your browser, not this app."
            : "Enter the code from the email here, or open the link in it. Either one signs you in."}
        </p>
        <form
          className="stack"
          onSubmit={(event) => {
            event.preventDefault();
            redeem(code);
          }}
        >
          <label>
            Code from the email
            <input
              className="login-code"
              required
              autoFocus
              name="code"
              type="text"
              autoComplete="one-time-code"
              inputMode="numeric"
              pattern="[0-9]*"
              maxLength={codeLength}
              readOnly={busy}
              value={code}
              onChange={(event) => enterCode(event.target.value)}
              onPaste={pasteCode}
            />
          </label>
          <button className="primary" disabled={busy}>
            Sign in
          </button>
        </form>
        <button
          type="button"
          className="link"
          disabled={busy}
          onClick={newCode}
        >
          {error ? "Request a new sign-in code" : "Use a different email"}
        </button>
      </>
    );
  } else {
    body = (
      <>
        <h1 id="sign-in-title">Log in to Drop</h1>
        <form className="stack" onSubmit={email}>
          <label>
            Email
            <input
              required
              autoFocus
              name="email"
              type="email"
              autoComplete="email"
              maxLength={254}
              defaultValue={address}
            />
          </label>
          {legal}
          <button
            className="primary"
            disabled={
              busy || (betaEnrollment && documents && !accepted)
            }
          >
            Email me a sign-in code
          </button>
        </form>
        <p className="hint">Drop is invite-only. <a href="#/waitlist">Join waitlist</a></p>
      </>
    );
  }
  return (
    <main className="sign-in">
      <section className="card" aria-labelledby="sign-in-title">
        <Brand />
        {body}
        <BetaLegalLinks documents={documents} />
        {error && (
          <p role="alert" className="error">
            {error}
          </p>
        )}
      </section>
    </main>
  );
}
