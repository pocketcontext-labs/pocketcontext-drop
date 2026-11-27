import { closeOnBackdropClick } from "./dialog";
import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { ACCOUNT_EMAIL, api, pb, type SignInOptions } from "./api";
import UserDirectory from "./UserDirectory";
import {CreateWorkspace,OfficialCatalog,OfficialReleases} from "./Official";
import AdminNodes from "./AdminNodes";
import AdminUsers from "./AdminUsers";
import AdminWaitlist from "./AdminWaitlist";
import Waitlist, {isWaitlistLeave, waitlistHash} from "./Waitlist";
import AppNew from "./AppNew";
import AppPage from "./AppPage";
import Apps from "./Apps";
import AppCatalog from "./AppCatalog";
import { readDeployLink, type DeployRecipe } from "./deployRecipes";
import AccountMenu from "./AccountMenu";
import Brand from "./Brand";
import BetaDeploy, { BetaLanding } from "./BetaDeploy";
import Credits from "./Credits";
import Connect from "./Connect";
import { dropApi, type Me } from "./dropApi";
import Login from "./Login";
import { LegalDocument, publishedLegalDocument } from "./LegalDocument";
import {
  appRouteFromHash,
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

function NavIcon({ path }: { path: string }) {
  return <svg className="nav-icon" aria-hidden="true" viewBox="0 0 24 24"
    fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
    <path d={path} />
  </svg>;
}

const mobileNav = window.matchMedia?.("(max-width: 640px)");
function subscribeMobileNav(notify: () => void) {
  mobileNav?.addEventListener("change", notify);
  return () => mobileNav?.removeEventListener("change", notify);
}

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
function SignInGate({
  hash,
  file,
  onSelect,
  options,
  recipe,
}: {
  recipe?: DeployRecipe;
  options: SignInOptions;
  hash: string;
  file: File | null;
  onSelect: (file: File) => void;
}) {
  const [login, setLogin] = useState(false);
  if (options.mode === "email") {
    if(options.official)return <Login documents={options.legal} initialEmail={inviteEmailFromHash(hash)} betaEnrollment={!!inviteEmailFromHash(hash)}/>;
    const deployAccess = !!file || !!recipe;
    const landing = ["", "#/", viewHashes.deploy].includes(hash);
    if (landing && !login && !file)
      return (
        <BetaLanding
          documents={options.legal}
          onLogin={() => setLogin(true)}
          onSelect={(chosen) => {
            onSelect(chosen);
            location.hash = viewHashes.deploy;
          }}
        />
      );
    return (
      <Login
        documents={options.legal}
        initialEmail={inviteEmailFromHash(hash)}
        deployAccess={deployAccess}
        betaEnrollment={
          deployAccess || hash === viewHashes.deploy || !!inviteEmailFromHash(hash)
        }
      />
    );
  }
  return <SignIn />;
}

function NotFound({admin = false}: {admin?: boolean}) {
  return (
    <section className="panel" aria-labelledby="not-found-title">
      <h2 id="not-found-title">Not found</h2>
      <p>
        This page does not exist. <a href={admin ? viewHashes["admin-users"] : viewHashes.apps}>{admin ? "Go to Users" : "Go to your apps"}</a>.
      </p>
    </section>
  );
}

function Workspace({
  fromSignIn,
  recipe,
  view,
  hash,
  file,
}: {
  fromSignIn: boolean;
  recipe?: DeployRecipe;
  view: View;
  hash: string;
  file: File | null;
}) {
  const email = accountEmail();
  // The account's role decides which pages it has; the server checks it on
  // every operation regardless.
  const moreSheet = useRef<HTMLDialogElement>(null);
  const mobile = useSyncExternalStore(subscribeMobileNav, () => mobileNav?.matches ?? false);
  const [me, setMe] = useState<Me | null>(null);
  const [meFailed, setMeFailed] = useState(false);
  useEffect(() => {
    let active = true;
    dropApi.me().then(
      (value) => {
        if (!active) return;
        if (fromSignIn && value.hosted && value.admin) location.hash = viewHashes[value.official_catalog?"admin-users":"admin-waitlist"];
        setMe(value);
      },
      () => {
        if (active) setMeFailed(true);
      },
    );
    return () => {
      active = false;
    };
  }, [fromSignIn]);
  useEffect(() => {
    if (!me?.hosted) return;
    let active = true;
    const update = () => { void dropApi.me().then(next => { if (active) setMe(next); }).catch(() => {}); };
    const timer = window.setInterval(update, 30_000);
    window.addEventListener("drop-credits-changed", update);
    return () => { active = false; window.clearInterval(timer); window.removeEventListener("drop-credits-changed", update); };
  }, [me?.hosted, me?.admin]);
  if (!me) return <Status>{meFailed ? <p role="alert">Could not load your account. Reload the page to retry.</p> : <p role="status">Loading account…</p>}</Status>;
  const hostedAdmin = me.admin && me.hosted === true;
  const defaultPage = ["", "#/", "#/login", "#/account"].includes(hash) || hash.startsWith("#/invite?");
  const administration = view === "admin-waitlist" || view === "admin-users" || view === "admin-nodes";
  let page;
  const developer = me.hosted === true && me.role === "developer";
  const app = view === "app" ? appRouteFromHash(hash) : null;
  if(hostedAdmin&&view==="admin-releases")page=<OfficialReleases onActivated={()=>setMe({...me,official_catalog:true})}/>;
  else if(hostedAdmin&&me.official_catalog&&(defaultPage||view==="admin-waitlist"||view==="admin-users"))page=<UserDirectory me={me} waiting={view==="admin-waitlist"}/>;
  else if(view==="admin-releases")page=<NotFound/>;
  else if (view === "custom-new") page = developer ? <CreateWorkspace privateBuild /> : <NotFound admin={hostedAdmin} />;
  else if (view === "credits") page = <Credits admin={me.admin} />;
  else if (hostedAdmin && (defaultPage || view === "admin-waitlist"))
    page = <AdminWaitlist account={me.id} />;
  else if (hostedAdmin && !administration) page = <NotFound admin />;
  else if (administration) {
    if (!me.admin || view === "admin-waitlist") page = <NotFound />;
    else page = view === "admin-users" ? <AdminUsers me={me} /> : <AdminNodes />;
  } else if (view === "connect") page = <Connect />;
  else if (me.hosted && (me.official_catalog || !developer) && ["catalog", "deploy", "app-new"].includes(view))
    page = <OfficialCatalog releaseSelection={me.release_selection === true} />;
  else if (view === "catalog") page = me.hosted ? <AppCatalog /> : <NotFound />;
  else if (view === "deploy" || (view === "app-new" && me.hosted))
    page = <BetaDeploy key={recipe ? hash : "generic"} recipe={recipe} initialFile={recipe ? null : file} account={me.id} />;
  else if (view === "app-new") page = <AppNew />;
  else if (app) page = <AppPage key={app.id} id={app.id} tab={app.tab} official={me.official_catalog} canDeployBinary={!me.hosted || developer} />;
  else page = <Apps developer={developer} beta={me.hosted === true} official={me.official_catalog} />;
  return (
    <div className="app">
      <header className="navbar-surface"><div className="topbar">
        <Brand />
        <nav className="nav" aria-label="Pages">
          {!hostedAdmin && <a
            href={viewHashes.apps}
            aria-current={
              view === "apps" || view === "app" || (view === "app-new" || view === "custom-new")
                ? "page"
                : undefined
            }
          >
            <NavIcon path="M3 10l9-7 9 7v10h-6v-7H9v7H3z" />Apps
          </a>}
          {me.hosted && !hostedAdmin && (
            <a href={viewHashes.catalog} aria-current={view === "catalog" ? "page" : undefined}><NavIcon path="M3 3h7v7H3z M14 3h7v7h-7z M3 14h7v7H3z M14 14h7v7h-7z" />Catalog</a>
          )}
          {hostedAdmin&&!me.official_catalog && (
            <a href={viewHashes["admin-waitlist"]} aria-current={defaultPage || view === "admin-waitlist" ? "page" : undefined}>
              <NavIcon path="M5 4h14 M5 20h14 M7 4v3l5 5-5 5v3 M17 4v3l-5 5 5 5v3" />Waitlist
            </a>
          )}
          {me?.admin && (
            <a
              href={viewHashes["admin-users"]}
              aria-current={view === "admin-users" || (me.official_catalog && (defaultPage||view==="admin-waitlist")) ? "page" : undefined}
            >
              <NavIcon path="M16 21v-2a6 6 0 0 0-12 0v2 M14 7a4 4 0 1 1-8 0 4 4 0 0 1 8 0 M18 5a4 4 0 0 1 0 7 M18 15a5 5 0 0 1 3 5" />Users
            </a>
          )}
          {hostedAdmin&&<a href={viewHashes["admin-releases"]} aria-current={view==="admin-releases"?"page":undefined}><NavIcon path="M3 7l9-4 9 4-9 4z M3 7v10l9 4 9-4V7 M12 11v10" />Releases</a>}
          {me?.admin && (
            <a
              href={viewHashes["admin-nodes"]}
              aria-current={view === "admin-nodes" ? "page" : undefined}
            >
              <NavIcon path="M3 3h18v7H3z M3 14h18v7H3z M6 7h2 M6 18h2" />Nodes
            </a>
          )}
          <a href={viewHashes.credits} aria-current={view === "credits" ? "page" : undefined}><NavIcon path="M3 5h18v14H3z M3 10h18 M6 15h4" />Credits</a>
          {!mobile && !hostedAdmin && <a
            href={viewHashes.connect}
            aria-current={view === "connect" ? "page" : undefined}
          >
            <NavIcon path="M8 8H5v4a7 7 0 0 0 14 0V8h-3 M9 4v5 M15 4v5 M12 19v3" />Connect
          </a>}
          <button className="mobile-more" type="button" aria-haspopup="dialog" aria-controls="mobile-account" onClick={() => {
            moreSheet.current?.showModal();
            moreSheet.current?.querySelector<HTMLElement>("#more-title")?.focus();
          }}>
            <svg aria-hidden="true" viewBox="0 0 24 24" width="20" height="20" fill="currentColor">
              <circle cx="4" cy="12" r="2" /><circle cx="12" cy="12" r="2" /><circle cx="20" cy="12" r="2" />
            </svg>
            More
          </button>
        </nav>
        {mobile ? (
          <dialog id="mobile-account" ref={moreSheet} aria-labelledby="more-title"
            onClick={closeOnBackdropClick}>
            <div className="sheet-heading">
              <h2 id="more-title" tabIndex={-1}>More</h2>
              <button type="button" className="primary" onClick={() => moreSheet.current?.close()}>Done</button>
            </div>
            {!hostedAdmin && <a href={viewHashes.connect} aria-current={view === "connect" ? "page" : undefined} onClick={() => moreSheet.current?.close()}>Connect an assistant</a>}
            <button className="secondary" type="button" onClick={() => api.logout()}>Sign out</button>
            {email && <p>{email}</p>}
          </dialog>
        ) : (
          <AccountMenu email={email || me.email || ""} />
        )}
      </div></header>
      <main className="content">
        {view !== "credits" && me.trial_remaining_seconds !== undefined && <p className="panel" role="status">
          {Math.ceil(me.trial_remaining_seconds / 60)} shared awake minutes left. Apps run locally until the buffer runs out, then stop with their data retained. A top-up resets the buffer to 60 minutes. <a href={viewHashes.credits}>Manage credits</a>
        </p>}
        {page}
      </main>
    </div>
  );
}

export default function App() {
  const [options, setOptions] = useState<SignInOptions | null>(null);
  const [failed, setFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let active = true;
    setFailed(false);
    api.loginMode().then(
      (value) => {
        if (active) setOptions(value);
      },
      () => {
        if (active) setFailed(true);
      },
    );
    return () => {
      active = false;
    };
  }, [attempt]);

  const [file, setFile] = useState<File | null>(null);
  const signedIn = useSyncExternalStore(subscribe, identity, () => "");
  useEffect(() => {
    if (signedIn) return () => setFile(null);
  }, [signedIn]);
  const initialIdentity = useRef(signedIn);
  useEffect(() => { if (!signedIn) initialIdentity.current = ""; }, [signedIn]);
  const [emailLink, setEmailLink] = useState(() => readEmailLoginLink());
  const [view, setView] = useState(() => viewFromHash(location.hash));
  const [hash, setHash] = useState(() => location.hash);
  useEffect(() => {
    if (signedIn && hash !== viewHashes.deploy) setFile(null);
  }, [signedIn, hash]);
  const deployLink = useMemo(() => readDeployLink(hash), [hash]);
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
      setHash(location.hash);
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
  const legalDocument = publishedLegalDocument(hash);
  if (legalDocument) return <LegalDocument html={legalDocument} />;
  if (!options) {
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
  if (options.mode === "email" && (hash === waitlistHash || isWaitlistLeave(hash)))
    return <Waitlist key={hash} hash={hash} />;
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
        documents={options.legal}
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
          setHash(target ?? "");
          setEmailLink(null);
        }}
      />
    );
  if (!options.official && deployLink.error) return <Status><h1>Invalid deployment link</h1><p role="alert">{deployLink.error}</p><a href={viewHashes.deploy}>Open deployment form</a></Status>;
  if (!signedIn)
    return <SignInGate recipe={deployLink.recipe} options={options} hash={hash} file={file} onSelect={setFile} />;
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
  return <Workspace recipe={deployLink.recipe} key={signedIn} fromSignIn={signedIn !== initialIdentity.current} view={view} hash={hash} file={file} />;
}
