import { useRef, useState, type ClipboardEvent, type FormEvent } from "react";
import { api, type PublishedLegal } from "./api";
import Waitlist, {
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
function validSignInAddress(input: HTMLInputElement): boolean {
  const address = input.value;
  const local = address.split("@", 1)[0];
  return input.validity.valid && /^[\x21-\x7e]{1,254}$/.test(address)
    && !local.startsWith(".") && !local.endsWith(".") && !local.includes("..");
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
  const waitlist = !deployAccess && access === "uninvited";
  const [needsConsent, setNeedsConsent] = useState(false);
  const [validEmail, setValidEmail] = useState(() => {
    const input = document.createElement("input");
    input.type = "email";
    input.required = true;
    input.value = initialEmail;
    return validSignInAddress(input);
  });
  const [emailTouched, setEmailTouched] = useState(false);
  const [resent, setResent] = useState(false);
  const [joined, setJoined] = useState(false);
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
  async function sendCode(value: string, joinWaitlist: boolean) {
    if (joinWaitlist) {
      await submitWaitlist("request", { email: value, terms_version: waitlistVersion });
    } else {
      const hash = location.hash;
      const id = await api.requestEmailLogin(value);
      const target = loginReturnHash(hash);
      if (target) rememberLoginReturn(target);
      else if (!hash.startsWith("#/login")) clearLoginReturn();
      setOtpId(id);
    }
    setCode("");
    setSentTo(value);
  }
  const email = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (busy || !validEmail) return;
    const value = address.trim();
    void run(async () => {
      if (access === "unknown") {
        const reply = await fetch("/api/drop/waitlist/request", {
          method: "POST", credentials: "omit", redirect: "error",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ email: value, check_access: true, ...(!deployAccess && { login_flow: true }) }),
        });
        if (reply.status === 400) throw new Error("Enter a valid email address.");
        if (reply.status === 429) throw new Error("Too many attempts. Try again later.");
        if (!reply.ok) throw new Error("Could not check access. Try again.");
        const result = await reply.json();
        setAccess(result.admitted ? "admitted" : "uninvited");
        const consentRequired = deployAccess ? Boolean(documents) : result.needs_consent === true;
        setNeedsConsent(consentRequired);
        if (!result.admitted || consentRequired) return;
      } else if ((needsConsent || waitlist) && !accepted) {
        return;
      }
      await sendCode(value, waitlist);
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
          await submitWaitlist("verify", { email: sentTo, code: value });
          clearLoginReturn();
          setJoined(true);
          return;
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
    setNeedsConsent(false);
    setResent(false);
    setEmailTouched(false);
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
          disabled={busy}
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
  ) : betaEnrollment || needsConsent ? (
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
            setAddress("");
            setValidEmail(false);
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
              A sign-in code and link are on their way to <strong>{sentTo}</strong>. They work for 15 minutes.
            </>
          )}
        </p>
        <p className="hint">
          {waitlist
            ? "Copy the code from the email, then paste or enter it here to finish joining the waitlist."
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
        <div className="waitlist-actions">
          <button className="link" disabled={busy} onClick={() => void run(async () => {
            await sendCode(sentTo, waitlist);
            setResent(true);
          })}>Resend code</button>
          <button className="link" disabled={busy} onClick={() => {
            newCode(); setAddress(""); setValidEmail(false);
          }}>Change email</button>
        </div>
        {resent && <p role="status">Check your inbox for the latest code.</p>}
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
        {waitlist && <p role="status">This email doesn’t have access yet.</p>}
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
              readOnly={busy || (deployAccess && access === "admitted")}
              value={address}
              onChange={event => {
                setAddress(event.target.value);
                setValidEmail(validSignInAddress(event.target));
                setAccess("unknown"); setAccepted(false); setNeedsConsent(false); setError("");
              }}
              onBlur={() => setEmailTouched(true)}
              aria-invalid={emailTouched && !!address && !validEmail}
            />
          </label>
          {emailTouched && !!address && !validEmail && <p role="alert" className="error">Enter a valid email address.</p>}
          {access === "admitted" && needsConsent && legal}
          {waitlist && <>
          <label className="check">
            <input
              type="checkbox"
              checked={accepted}
              disabled={busy}
              onChange={(event) => setAccepted(event.target.checked)}
            />
            <span>
              I accept the{" "}
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
              busy || !validEmail || ((waitlist || needsConsent) && !accepted)
            }
          >
            {deployAccess && access === "unknown" ? "Check access" : waitlist
              ? "Join the waitlist"
              : "Email me a sign-in code"}
          </button>
        </form>
        {deployAccess && <p className="hint">
          Drop is invite-only. <a href="#/waitlist">Join waitlist</a>
        </p>}
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
