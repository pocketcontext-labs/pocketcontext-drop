import { useEffect, useState, useSyncExternalStore } from "react";
import { ACCOUNT_EMAIL, api, pb, type AuthMode } from "./api";
import AdminUsers from "./AdminUsers";
import Apps from "./Apps";
import Brand from "./Brand";
import Connect from "./Connect";
import { dropApi, type Me } from "./dropApi";
import Login from "./Login";
import {
  browserNavigation,
  inviteEmailFromHash,
  isSigninHash,
  oauthTargetFromHash,
  readEmailLoginLink,
  takeLoginReturn,
  viewFromHash,
  viewHashes,
  type View,
} from "./routes";
import SignIn from "./SignIn";

function subscribe(change: () => void) {
  return pb.authStore.onChange(change);
}
function identity() {
  return pb.authStore.isValid && pb.authStore.record?.collectionName === "users"
    ? pb.authStore.record.id
    : "";
}
/**
 * Hosted accounts all share one name, so the reader shows the email. The
 * local account's reserved address is never shown.
 */
function accountEmail() {
  const email = pb.authStore.record?.email;
  return typeof email === "string" && email !== ACCOUNT_EMAIL ? email : "";
}
const currentSigninHash = () =>
  isSigninHash(location.hash) ? location.hash : "";

function Status({ children }: { children: React.ReactNode }) {
  return (
    <main className="sign-in">
      <section className="card">
        <Brand />
        {children}
      </section>
    </main>
  );
}

/** Email sign-in on the hosted origin; the project's password locally. */
function SignInGate() {
  const [mode, setMode] = useState<AuthMode | null>(null);
  const [failed, setFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let active = true;
    setFailed(false);
    api.loginMode().then(
      (value) => {
        if (active) setMode(value);
      },
      () => {
        if (active) setFailed(true);
      },
    );
    return () => {
      active = false;
    };
  }, [attempt]);
  if (mode === "email")
    return <Login initialEmail={inviteEmailFromHash(location.hash)} />;
  if (mode === "password") return <SignIn />;
  return (
    <Status>
      {failed ? (
        <>
          <p role="alert" className="error">
            Unable to load sign-in options. Please try again.
          </p>
          <button
            className="primary"
            onClick={() => setAttempt((value) => value + 1)}
          >
            Try again
          </button>
        </>
      ) : (
        <p role="status">Loading sign-in options…</p>
      )}
    </Status>
  );
}

function NotFound() {
  return (
    <section className="panel" aria-labelledby="not-found-title">
      <h2 id="not-found-title">Not found</h2>
      <p>
        This page does not exist. <a href={viewHashes.apps}>Go to your apps</a>.
      </p>
    </section>
  );
}

function Workspace({ view }: { view: View }) {
  const email = accountEmail();
  // The account's role decides which pages it has; the server checks it on
  // every operation regardless.
  const [me, setMe] = useState<Me | null>(null);
  const [meFailed, setMeFailed] = useState(false);
  useEffect(() => {
    let active = true;
    dropApi.me().then(
      (value) => {
        if (active) setMe(value);
      },
      () => {
        if (active) setMeFailed(true);
      },
    );
    return () => {
      active = false;
    };
  }, []);
  let page;
  if (view === "connect") page = <Connect />;
  else if (view === "admin-users")
    page = me?.admin ? (
      <AdminUsers me={me} />
    ) : me || meFailed ? (
      <NotFound />
    ) : (
      <p role="status" className="quiet">
        Loading…
      </p>
    );
  else page = <Apps />;
  return (
    <div className="app">
      <header className="topbar">
        <Brand />
        <nav className="nav" aria-label="Pages">
          <a
            href={viewHashes.apps}
            aria-current={view === "apps" ? "page" : undefined}
          >
            Apps
          </a>
          {me?.admin && (
            <a
              href={viewHashes["admin-users"]}
              aria-current={view === "admin-users" ? "page" : undefined}
            >
              Users
            </a>
          )}
          <a
            href={viewHashes.connect}
            aria-current={view === "connect" ? "page" : undefined}
          >
            Connect
          </a>
        </nav>
        <div className="account">
          {email && <span title={email}>{email}</span>}
          <button
            type="button"
            className="secondary"
            onClick={() => api.logout()}
          >
            Sign out
          </button>
        </div>
      </header>
      <main className="content">{page}</main>
    </div>
  );
}

export default function App() {
  const signedIn = useSyncExternalStore(subscribe, identity, () => "");
  const [emailLink, setEmailLink] = useState(() => readEmailLoginLink());
  const [view, setView] = useState(() => viewFromHash(location.hash));
  const [signinHash, setSigninHash] = useState(currentSigninHash);
  const [returning, setReturning] = useState(false);
  const [checkFailed, setCheckFailed] = useState(false);
  const [checkAttempt, setCheckAttempt] = useState(0);
  const oauthTarget = signinHash ? oauthTargetFromHash(signinHash) : null;
  useEffect(() => {
    const receive = () => {
      const link = readEmailLoginLink();
      if (link) setEmailLink(link);
      setSigninHash(currentSigninHash());
      setView(viewFromHash(location.hash));
    };
    window.addEventListener("hashchange", receive);
    return () => window.removeEventListener("hashchange", receive);
  }, []);
  // The link's credentials never stay in the address bar.
  useEffect(() => {
    if (emailLink)
      history.replaceState(
        null,
        "",
        location.pathname + location.search + "#/login",
      );
  }, [emailLink]);
  // Opening the reader, or coming back to it, renews the sign-in (throttled);
  // a rejected token signs out, as any other request would.
  useEffect(() => {
    if (!signedIn || emailLink || oauthTarget) return;
    const renew = () => void api.refreshSession().catch(() => {});
    renew();
    window.addEventListener("focus", renew);
    return () => window.removeEventListener("focus", renew);
  }, [signedIn, emailLink, oauthTarget]);
  // An unusable return target is ignored, never followed.
  useEffect(() => {
    if (!signinHash || oauthTarget) return;
    history.replaceState(null, "", location.pathname + location.search + "#/");
    setSigninHash("");
  }, [signinHash, oauthTarget]);
  // The stored token only looks valid until the server agrees; without this
  // check a rejected token would bounce between consent and this route.
  useEffect(() => {
    if (!oauthTarget || !signedIn || emailLink) return;
    let active = true;
    setCheckFailed(false);
    api.refreshSession(true).then(
      () => {
        if (active) browserNavigation.replace(oauthTarget);
      },
      () => {
        if (active) setCheckFailed(true);
      },
    );
    return () => {
      active = false;
    };
  }, [oauthTarget, signedIn, emailLink, checkAttempt]);
  if (returning)
    return (
      <Status>
        <p role="status">Returning to the connection request…</p>
      </Status>
    );
  if (emailLink)
    return (
      <Login
        key={emailLink.otpId}
        emailLink={emailLink}
        onComplete={() => {
          const target = takeLoginReturn();
          const oauth = target && oauthTargetFromHash(target);
          if (oauth) {
            setReturning(true);
            browserNavigation.replace(oauth);
            return;
          }
          history.replaceState(null, "", location.pathname + (target ?? "#/"));
          setView(viewFromHash(target ?? ""));
          setEmailLink(null);
        }}
      />
    );
  if (!signedIn) return <SignInGate />;
  if (oauthTarget)
    return (
      <Status>
        {checkFailed ? (
          <>
            <p role="alert" className="error">
              Unable to check your sign-in. Please try again.
            </p>
            <button
              className="primary"
              onClick={() => setCheckAttempt((value) => value + 1)}
            >
              Try again
            </button>
          </>
        ) : (
          <p role="status">Returning to the connection request…</p>
        )}
      </Status>
    );
  // Remount per session so no state survives a change of identity.
  return <Workspace key={signedIn} view={view} />;
}
