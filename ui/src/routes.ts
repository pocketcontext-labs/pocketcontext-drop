// Hash routes of the reader (as in Crew): `#/apps` (the default), `#/connect`,
// `#/admin/users` (admins only; anyone else sees Not found),
// `#/invite?email=…` (the sign-in URL of an invite: the sign-in page with the
// address filled in), `#/login?otpId=…&otp=…` (the link of a sign-in email)
// and `#/signin?next=/oauth/authorize?…` (OAuth consent sends the browser here
// to sign in and come back).

export type View = "apps" | "connect" | "admin-users";
export const viewHashes: Record<View, string> = {
  apps: "#/apps",
  connect: "#/connect",
  "admin-users": "#/admin/users",
};
/** The page of a hash; anything unknown is the apps page. */
export function viewFromHash(hash: string): View {
  if (/^#\/connect(?:\?.*)?$/.test(hash)) return "connect";
  if (/^#\/admin\/users(?:\?.*)?$/.test(hash)) return "admin-users";
  return "apps";
}

/** The address of an invite's sign-in URL (`#/invite?email=…`), or "". */
export function inviteEmailFromHash(hash: string): string {
  const match = /^#\/invite\?(.*)$/.exec(hash);
  if (!match) return "";
  const emails = new URLSearchParams(match[1]).getAll("email");
  const email = emails.length === 1 ? emails[0].trim() : "";
  // Printable ASCII without spaces, as the server accepts for invites.
  return email.length <= 254 && /^[\x21-\x7e]+@[\x21-\x7e]+$/.test(email)
    ? email
    : "";
}

export type EmailLoginLink = { otpId: string; password: string };
/**
 * The credentials of a sign-in email link. Only the fragment carries them;
 * reading it never redeems a link. A malformed link gives empty values.
 */
export function readEmailLoginLink(
  hash = location.hash,
): EmailLoginLink | null {
  const [path, query = ""] = hash.replace(/^#\/?/, "").split("?");
  if (path !== "login") return null;
  const params = new URLSearchParams(query);
  const otpId = params.get("otpId") || "";
  const password = params.get("otp") || "";
  if (
    params.getAll("otpId").length !== 1 ||
    params.getAll("otp").length !== 1 ||
    !/^[a-zA-Z0-9]{1,255}$/.test(otpId) ||
    !/^[0-9]{4,71}$/.test(password)
  ) {
    return { otpId: "", password: "" };
  }
  return { otpId, password };
}

// The OAuth return path is fixed: only the query (validated by the consent
// endpoint) can vary.
const signinPrefix = "#/signin?next=";
const oauthTargetMaxLength = 16384;

/** Accepts only `/oauth/authorize?<query>` on this origin. */
export function safeOAuthTarget(raw: unknown): string | null {
  if (typeof raw !== "string" || raw.length > oauthTargetMaxLength) return null;
  if (!/^\/oauth\/authorize\?[^\s\\#]+$/.test(raw)) return null;
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(raw)) return null;
  try {
    const url = new URL(raw, location.origin);
    return url.origin === location.origin &&
      url.pathname === "/oauth/authorize" &&
      url.pathname + url.search === raw
      ? raw
      : null;
  } catch {
    return null;
  }
}

export const isSigninHash = (hash: string) => /^#\/signin(?:\?|$)/.test(hash);

/** The validated return target of a `#/signin?next=` address. */
export function oauthTargetFromHash(hash: string): string | null {
  const match = /^#\/signin\?(.*)$/.exec(hash);
  if (!match) return null;
  const next = new URLSearchParams(match[1]).getAll("next");
  return next.length === 1 ? safeOAuthTarget(next[0]) : null;
}

const signinHash = (target: string) =>
  signinPrefix + encodeURIComponent(target);

/** Seam for tests: jsdom cannot perform this navigation. */
export const browserNavigation = {
  replace: (target: string) => location.replace(target),
};

// An emailed sign-in link usually opens in a new tab, so the place to return
// to has to outlive this one. It lasts as long as the email it accompanies.
const returnKey = "drop.reader.login_return";
const returnLifetimeMs = 15 * 60 * 1000;

export function clearLoginReturn() {
  try {
    localStorage.removeItem(returnKey);
  } catch {
    /* Optional storage. */
  }
}

/** The normalized place to return to after sign-in, or null for the default page. */
export function loginReturnHash(hash: string): string | null {
  if (isSigninHash(hash)) {
    const target = oauthTargetFromHash(hash);
    return target ? signinHash(target) : null;
  }
  const view = viewFromHash(hash);
  return view === "apps" ? null : viewHashes[view];
}

export function rememberLoginReturn(hash: string) {
  try {
    localStorage.setItem(
      returnKey,
      JSON.stringify({ hash, expires: Date.now() + returnLifetimeMs }),
    );
  } catch {
    /* Optional storage: sign-in still works and lands on the apps. */
  }
}

/** Only the exact forms loginReturnHash writes; anything else in storage is refused. */
function isStoredReturn(hash: string): boolean {
  if (hash === viewHashes.connect || hash === viewHashes["admin-users"])
    return true;
  const target = oauthTargetFromHash(hash);
  return target !== null && hash === signinHash(target);
}

/** Returns a validated return hash at most once; the stored value is always cleared. */
export function takeLoginReturn(): string | null {
  let raw: string | null = null;
  try {
    raw = localStorage.getItem(returnKey);
  } catch {
    return null;
  }
  clearLoginReturn();
  if (!raw) return null;
  try {
    const { hash, expires } = JSON.parse(raw) as {
      hash?: unknown;
      expires?: unknown;
    };
    const now = Date.now();
    return typeof hash === "string" &&
      isStoredReturn(hash) &&
      typeof expires === "number" &&
      expires > now &&
      expires <= now + returnLifetimeMs
      ? hash
      : null;
  } catch {
    return null;
  }
}
