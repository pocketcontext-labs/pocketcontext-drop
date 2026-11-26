import { useRef, useState, type ClipboardEvent, type FormEvent } from "react";
import { api, type PublishedLegal } from "./api";
import Waitlist, {
  InvalidWaitlistCode,
  legalBase,
  submitWaitlist,
  waitlistVersion,
} from "./Waitlist";
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
  deployAccess = false,
  documents,
}: {
  emailLink?: EmailLoginLink;
  onComplete?: () => void;
  /** The address of an invite's sign-in URL. */
  initialEmail?: string;
  betaEnrollment?: boolean;
  deployAccess?: boolean;
  documents?: PublishedLegal;
}) {
  const [access, setAccess] = useState<"unknown" | "admitted" | "uninvited" | "join">("unknown");
  const [accepted, setAccepted] = useState(false);
  const [waitlist, setWaitlist] = useState(false);
  const [joined, setJoined] = useState(false);
  const consent = useRef<HTMLInputElement>(null);
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
    if (betaEnrollment && documents && !accepted && !waitlist && (!deployAccess || access === "admitted")) return;
    const form = new FormData(event.currentTarget);
    const value = String(form.get("email")).trim();
    const hash = location.hash;
    setAddress(value);
    void run(async () => {
      if (deployAccess && access === "unknown") {
        const reply = await fetch("/api/drop/waitlist/request", {
          method: "POST", credentials: "omit", redirect: "error",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ email: value, check_access: true }),
        });
        if (!reply.ok) throw new Error(reply.status === 429 ? "Too many attempts. Try again later." : "Could not check access. Try again.");
        const result = await reply.json();
        setAccess(result.admitted ? "admitted" : "uninvited");
        if (!result.admitted || documents) return;
      }
      const [id] = await Promise.all([
        api.requestEmailLogin(value),
        waitlist &&
          submitWaitlist("request", {
            email: value,
            terms_version: waitlistVersion,
          }),
      ]);
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
        if (waitlist) {
          try {
            await submitWaitlist("verify", { email: sentTo, code: value });
            clearLoginReturn();
            setJoined(true);
            return;
          } catch (error) {
            if (!(error instanceof InvalidWaitlistCode)) throw error;
          }
        }
        if (accepted && documents)
          await api.loginEmailCode(otpId, value, documents.termsVersion);
        else await api.loginEmailCode(otpId, value);
      } catch (e) {
        setCode("");
        const failure = e as { status?: number; response?: { code?: string } };
        if (
          failure?.status === 428 &&
          failure.response?.code === "terms_required"
        ) {
          newCode();
          throw new Error(
            "To activate your invitation, accept the Beta Terms of Service and request a fresh sign-in code.",
          );
        }
        if (failure?.status === 400 || failure?.status === 401)
          throw new Error(
            "This code is incorrect or has expired. Check it or request a new code.",
          );
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
    if (!waitlist && digits.length === codeLength) redeem(digits);
  };
  const pasteCode = (event: ClipboardEvent<HTMLInputElement>) => {
    // maxLength would cut "123 456" before the spaces are stripped.
    event.preventDefault();
    enterCode(event.clipboardData.getData("text"));
  };
  const newCode = () => {
    setWaitlist(false);
    setAccess("unknown");
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
          await api.loginEmail(
            link.otpId,
            link.password,
            documents.termsVersion,
          );
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
  if (joined) {
    body = (
      <>
        <h1 id="sign-in-title">You’re on the waitlist</h1>
        <p>If a place opens for you, we’ll send an invitation to {sentTo}.</p>
        <button
          className="link"
          onClick={() => {
            setJoined(false);
            setWaitlist(false);
            newCode();
          }}
        >
          Use a different email
        </button>
      </>
    );
  } else if (link) {
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
          {waitlist ? (
            <>
              Check <strong>{sentTo}</strong> for your six-digit verification
              code.
            </>
          ) : (
            <>
              If {sentTo} has a Drop account or an invitation, a sign-in code
              and link are on their way. They work for 15 minutes.
            </>
          )}
        </p>
        <p className="hint">
          {waitlist
            ? "Copy the code from the email, then paste or enter it here to finish joining the waitlist. If you already have access, your sign-in code opens Drop."
            : isStandaloneApp()
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
          <button
            className="primary"
            disabled={busy || (waitlist && code.length !== codeLength)}
          >
            {waitlist ? "Verify and join the waitlist" : "Sign in"}
          </button>
        </form>
        <button
          type="button"
          className="link"
          disabled={busy}
          onClick={newCode}
        >
          {error
            ? waitlist
              ? "Request a new verification code"
              : "Request a new sign-in code"
            : "Use a different email"}
        </button>
        {!deployAccess && !waitlist && (
          <button
            className="link"
            disabled={busy}
            onClick={() => {
              newCode();
              requestAnimationFrame(() => consent.current?.focus());
            }}
          >
            Join the waitlist with this email
          </button>
        )}
      </>
    );
  } else if (deployAccess && access === "join") {
    return <Waitlist hash="#/waitlist" initialEmail={address} onReturn={newCode} />;
  } else if (deployAccess && access === "uninvited") {
    body = <>
      <h1 id="sign-in-title">Private beta, join the waitlist?</h1>
      <p>{address} does not have access yet.</p>
      <button className="primary" onClick={() => setAccess("join")}>Join the waitlist</button>
      <button className="link" onClick={newCode}>No, use another email</button>
    </>;
  } else {
    body = (
      <>
        <h1 id="sign-in-title">{deployAccess ? "Deploy your app" : "Log in to Drop"}</h1>
        {deployAccess && access === "admitted" && <p role="status">This email has access. Verify it to continue deploying.</p>}
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
              key={address}
              readOnly={busy || (deployAccess && access === "admitted")}
              defaultValue={address}
            />
          </label>
          {(!deployAccess || access === "admitted") && legal}
          {!deployAccess && <>
          <label className="check">
            <input
              ref={consent}
              type="checkbox"
              checked={waitlist}
              disabled={busy}
              onChange={(event) => setWaitlist(event.target.checked)}
            />
            <span>
              Join the waitlist if I don’t have access. I accept the{" "}
              <a href={legalBase + "/terms"} target="_blank" rel="noreferrer">
                Waitlist Terms
              </a>
              .
            </span>
          </label>
          <p className="hint">
            The{" "}
            <a href={legalBase + "/privacy"} target="_blank" rel="noreferrer">
              Waitlist Privacy Notice
            </a>{" "}
            explains how Drop uses your email.
          </p>
          </>}
          <button
            className="primary"
            disabled={
              busy || (betaEnrollment && documents && !accepted && !waitlist && (!deployAccess || access === "admitted"))
            }
          >
            {deployAccess && access === "unknown" ? "Check access" : waitlist
              ? "Email me a verification code"
              : "Email me a sign-in code"}
          </button>
        </form>
        <p className="hint">
          Drop is invite-only. <a href="#/waitlist">Join waitlist</a>
        </p>
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
