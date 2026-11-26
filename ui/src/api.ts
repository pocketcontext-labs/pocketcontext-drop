import PocketBase, {
  BaseAuthStore,
  LocalAuthStore,
  type RecordAuthResponse,
  type SendOptions,
} from "pocketbase";

// The SDK synchronizes this application's credentials across same-origin tabs.
// The single-use sign-in page (/bundle/login) writes the same key.
export const sessionKey = "drop.reader.auth";
/**
 * The sign-in identity of the project's one account (Identity.LocalAccountEmail in
 * internal/droppolicy/policy.go). Nobody types or sees it: the reader asks only for
 * the password.
 */
export const ACCOUNT_EMAIL = "account@drop.invalid";
function browserAuthStore(): BaseAuthStore {
  try {
    const probe = `${sessionKey}.probe.${Math.random()}`;
    window.localStorage.setItem(probe, "1");
    window.localStorage.removeItem(probe);
    return new LocalAuthStore(sessionKey);
  } catch {
    return new BaseAuthStore();
  }
}
const authStore = browserAuthStore();
// The SDK handles named keys, but localStorage.clear() emits a null key.
if (typeof window !== "undefined" && authStore instanceof LocalAuthStore) {
  window.addEventListener("storage", (event) => {
    if (event.storageArea !== window.localStorage) return;
    // Even an empty-to-empty logout must fence a sign-in that is still pending.
    if (event.key === null || event.key === sessionKey) cancelLogin();
    if (event.key === null) authStore.clear();
  });
}
if (authStore.record?.collectionName !== "users" || !authStore.isValid)
  authStore.clear();

/** Incremented whenever the signed-in identity changes, including sign-out. */
export let sessionGeneration = 0;
function identityKey() {
  return authStore.token
    ? `${authStore.record?.collectionName}:${authStore.record?.id}`
    : "";
}
let activeIdentity = identityKey();
let realtimeCleanup: Promise<void> = Promise.resolve();
authStore.onChange(() => {
  const next = identityKey();
  if (next === activeIdentity) {
    // A sibling tab already renewed this identity; do not renew it again on focus.
    if (next) {
      refreshedAt = Date.now();
      refreshedToken = authStore.token;
    }
    return;
  }
  activeIdentity = next;
  sessionGeneration++;
  realtimeCleanup = realtimeCleanup
    .then(() => pb.realtime.unsubscribe())
    .catch(() => {});
});

export const pb = new PocketBase(
  typeof window === "undefined" ? "http://localhost" : window.location.origin,
  authStore,
);
// Concurrent reads share an endpoint but must not cancel each other.
pb.autoCancellation(false);
// Reject fully parsed responses from a previous identity before callers see them.
const send = pb.send.bind(pb);
pb.send = async <T = unknown>(
  path: string,
  options: SendOptions = {},
): Promise<T> => {
  const generation = sessionGeneration;
  const token = authStore.token;
  const identity = identityKey();
  try {
    const result = await send<T>(path, options);
    if (generation !== sessionGeneration || identity !== identityKey())
      throw new Error("Session changed");
    return result;
  } catch (error) {
    const { status, response } = error as {
      status?: number;
      response?: { code?: unknown };
    };
    // An operation the account's role does not allow is refused with 403
    // "forbidden"; the session itself is fine.
    const refusedOperation =
      path === "/api/drop/op" &&
      status === 403 &&
      response?.code === "forbidden";
    if (
      generation === sessionGeneration &&
      token &&
      token === authStore.token &&
      (status === 401 || status === 403) &&
      !refusedOperation
    )
      authStore.clear();
    throw error;
  }
};

export const NAME_LIMIT = 80;
export function validName(name: string): string {
  const trimmed = name.trim();
  if (!trimmed) throw new Error("Name the app first");
  if (trimmed.length > NAME_LIMIT)
    throw new Error(`An app name has at most ${NAME_LIMIT} characters`);
  return trimmed;
}
/** The slug rule of the apps migration (pb_migrations/1820000200_apps.js). */
export const SLUG_PATTERN = /^[a-z][a-z0-9-]{1,30}[a-z0-9]$/;
export function validSlug(slug: string): string {
  const trimmed = slug.trim();
  if (!SLUG_PATTERN.test(trimmed))
    throw new Error(
      "A slug has 3-32 lowercase letters, digits and hyphens, starts with a letter and does not end with a hyphen",
    );
  return trimmed;
}
/** Notify on any change to the apps. Returns an unsubscribe. */
export async function watchApps(notify: () => void): Promise<() => void> {
  const generation = sessionGeneration;
  const unsubscribe = await pb.collection("apps").subscribe("*", () => {
    if (generation === sessionGeneration) notify();
  });
  return () => void unsubscribe().catch(() => {});
}

let pendingSignIn: { cancel: (message?: string) => void } | null = null;
/**
 * Run one sign-in exchange on an isolated client, then save its result only if
 * nothing changed meanwhile: no cancellation, newer attempt, sign-out or other
 * account (in this or another tab).
 */
async function signIn(
  exchange: (client: PocketBase) => Promise<RecordAuthResponse>,
): Promise<void> {
  pendingSignIn?.cancel();
  const generation = sessionGeneration,
    token = authStore.token;
  const client = new PocketBase(pb.baseURL, new BaseAuthStore());
  client.autoCancellation(false);
  let ended = false;
  let rejectAttempt: (error: Error) => void = () => {};
  const attempt = {
    cancel(message = "Sign-in cancelled. You can try again.") {
      if (ended) return;
      ended = true;
      // This isolated client owns only this attempt.
      client.cancelAllRequests();
      rejectAttempt(Object.assign(new Error(message), { isAbort: true }));
    },
  };
  pendingSignIn = attempt;
  try {
    const result = await new Promise<RecordAuthResponse>((resolve, reject) => {
      rejectAttempt = reject;
      exchange(client).then(resolve, reject);
    });
    if (
      ended ||
      pendingSignIn !== attempt ||
      generation !== sessionGeneration ||
      token !== authStore.token
    )
      throw new Error("Session changed");
    if (
      typeof result?.token !== "string" ||
      !result.token ||
      result.record?.collectionName !== "users"
    )
      throw new Error("Invalid account identity");
    refreshedAt = Date.now();
    refreshedToken = result.token;
    authStore.save(result.token, result.record);
  } finally {
    ended = true;
    if (pendingSignIn === attempt) pendingSignIn = null;
  }
}
/** Cancel a pending sign-in and fence late responses. */
export function cancelLogin(): void {
  pendingSignIn?.cancel();
}
/** Sign in to the project's one account with its password. */
export async function loginPassword(password: string): Promise<void> {
  await signIn((client) =>
    client.collection("users").authWithPassword(ACCOUNT_EMAIL, password),
  );
}
/** Normalize a one-time code from `drop login-code` or an `open` link. */
export function normalizeCode(input: string): string {
  let code = input.trim();
  // Accept a whole pasted sign-in link; only its fragment is the code.
  const hash = code.indexOf("#");
  if (hash >= 0) code = code.slice(hash + 1);
  if (!/^[A-Za-z0-9_-]{8,256}$/.test(code))
    throw new Error("Enter the sign-in code exactly as it was given to you");
  return code;
}
/** Redeem a single-use sign-in code (no password passes through anyone else). */
export async function loginCode(input: string): Promise<void> {
  const code = normalizeCode(input);
  await signIn((client) =>
    client.send<RecordAuthResponse>("/api/bundle/redeem", {
      method: "POST",
      body: { code },
    }),
  );
}

export interface PublishedLegal {
  termsURL: string;
  privacyURL: string;
  termsVersion: string;
}
export interface SignInOptions {
  mode: "email" | "password";
  legal?: PublishedLegal;
}

function publishedLegal(headers: Headers): PublishedLegal | undefined {
  const termsURL = headers.get("X-Drop-Terms-URL");
  const privacyURL = headers.get("X-Drop-Privacy-URL");
  const termsVersion = headers.get("X-Drop-Terms-Version");
  if (!termsURL || !privacyURL || termsURL === privacyURL || !termsVersion ||
      !/^[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$/.test(termsVersion)) return;
  try {
    for (const value of [termsURL, privacyURL]) {
      const url = new URL(value);
      if (url.protocol !== "https:" || url.username || url.password) return;
    }
  } catch { return; }
  return { termsURL, privacyURL, termsVersion };
}

/** Read the current published legal versions and login mode from the same response. */
export async function loginMode(): Promise<SignInOptions> {
  const response = await fetch("/api/bundle/auth-mode", {
    cache: "no-store", credentials: "omit", redirect: "error",
  });
  if (!response.ok) throw new Error("Unable to load sign-in options. Please try again.");
  const { mode } = await response.json();
  if (mode === "password") return { mode };
  if (mode !== "email") throw new Error("Unable to load sign-in options. Please try again.");
  return { mode, legal: publishedLegal(response.headers) };
}
/** Returns the login request id that the emailed code is bound to. */
export async function requestEmailLogin(email: string): Promise<string> {
  const client = new PocketBase(pb.baseURL, new BaseAuthStore());
  const result = await client.collection("users").requestOTP(email.trim());
  return result.otpId;
}
/** Redeems the link of a sign-in email. */
export async function loginEmail(
  otpId: string,
  password: string,
  termsVersion?: string,
): Promise<void> {
  await signIn((client) =>
    client.collection("users").authWithOTP(otpId, password, {
      headers: termsVersion ? { "X-Drop-Accept-Terms": termsVersion } : {},
    }),
  );
}
/** Redeems the 6-digit code from the sign-in email, for the app that asked for it. */
export async function loginEmailCode(
  otpId: string,
  code: string,
  termsVersion?: string,
): Promise<void> {
  await signIn((client) =>
    client.send<RecordAuthResponse>(
      "/api/collections/users/auth-with-otp-code",
      { method: "POST", body: { otpId, code }, headers: termsVersion ? { "X-Drop-Accept-Terms": termsVersion } : {} },
    ),
  );
}

/** Where MCP clients connect: the hosted endpoint, or none locally. */
export type Connection = { kind: "mcp"; url: string } | { kind: "local" };
/**
 * Only the hosted origin (email sign-in) serves MCP; the local server never
 * does, so it is not asked (a 404 would be a console error). MCP clients
 * discover the endpoint from the same public document read here; any answer
 * other than this origin's /mcp proves nothing, so it throws.
 */
export async function connection(): Promise<Connection> {
  if ((await api.loginMode()).mode === "password") return { kind: "local" };
  const response = await fetch("/.well-known/oauth-protected-resource", {
    cache: "no-store",
    credentials: "omit",
  });
  if (!response.ok) throw new Error("Connection details unavailable");
  const { resource } = (await response.json()) as { resource?: unknown };
  if (typeof resource !== "string")
    throw new Error("Connection details unavailable");
  const url = new URL(resource);
  if (
    url.protocol !== "https:" ||
    url.host !== location.host ||
    url.pathname !== "/mcp"
  )
    throw new Error("Connection details unavailable");
  return { kind: "mcp", url: resource };
}
/** An MCP client the signed-in account connected through OAuth. */
export type ConnectedApp = { id: string; name: string; created: number };
async function connectionsRequest<T>(body?: { id: string }): Promise<T> {
  const response = await fetch("/oauth/connections", {
    method: body ? "POST" : "GET",
    credentials: "omit",
    redirect: "error",
    headers: {
      Authorization: `Bearer ${pb.authStore.token}`,
      Accept: "application/json",
      "Content-Type": "application/json",
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!response.ok) throw new Error("Connected apps are unavailable.");
  return response.json() as Promise<T>;
}
export async function connectedApps(): Promise<ConnectedApp[]> {
  const { connections } = await connectionsRequest<{
    connections: ConnectedApp[];
  }>();
  if (!Array.isArray(connections))
    throw new Error("Connected apps are unavailable.");
  return connections;
}
/** Disconnect one app: its tokens stop working at once. */
export async function revokeConnectedApp(id: string): Promise<void> {
  await connectionsRequest({ id });
}

let refreshedAt = 0;
let refreshedToken = "";
/**
 * Renew the token at most every five minutes; a rejected token signs out.
 * `force` asks the server even when this token was renewed moments ago.
 */
export async function refreshSession(force = false): Promise<void> {
  const initiatingToken = pb.authStore.token;
  if (!initiatingToken) throw new Error("Please sign in to continue");
  const generation = sessionGeneration;
  if (
    !force &&
    initiatingToken === refreshedToken &&
    Date.now() - refreshedAt < 300000
  )
    return;
  // Save explicitly: the SDK authRefresh helper saves even after a user signs out.
  const result = await pb.send<RecordAuthResponse>(
    "/api/collections/users/auth-refresh",
    { method: "POST", headers: { Authorization: initiatingToken } },
  );
  if (
    pb.authStore.token !== initiatingToken ||
    generation !== sessionGeneration
  )
    return;
  if (result.record.collectionName !== "users")
    throw new Error("Invalid account identity");
  refreshedAt = Date.now();
  refreshedToken = result.token;
  pb.authStore.save(result.token, result.record);
}
export function logout(): void {
  cancelLogin();
  pb.authStore.clear();
}

export const api = {
  cancelLogin,
  loginPassword,
  loginCode,
  refreshSession,
  logout,
  loginMode,
  requestEmailLogin,
  loginEmail,
  loginEmailCode,
  watchApps,
  connection,
  connectedApps,
  revokeConnectedApp,
};
