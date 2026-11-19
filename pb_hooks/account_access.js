// Drop holds several accounts in hosted mode: the owner (OWNER_EMAIL) and the
// accounts core creates when Hooks.AllowSignIn admits an address. Local
// serving keeps exactly one account (core refuses a database that has held
// hosted accounts). Every API still requires a token.

function request(e) {
  // PocketBase treats an invalid bearer as anonymous; make expired or revoked
  // sessions an explicit failure rather than an anonymous request.
  if (!e.auth && e.request.header.get("Authorization")) {
    throw new UnauthorizedError("The session is invalid or expired.");
  }
  return e.next();
}

// Model-level, so it also covers superuser, dashboard and internal saves.
// Core decides which accounts exist: the local account, the hosted owner and
// the accounts AllowSignIn admits.
function create(e) {
  return e.next();
}

// Core prunes accounts that never signed in (unverified). An account that has
// signed in is never deleted: it keeps its history and is suspended instead.
function remove(e) {
  if (e.record.verified()) {
    throw new ForbiddenError("An account that has signed in cannot be deleted.");
  }
  return e.next();
}

function realtime(e) {
  // Connection setup is anonymous; every record subscription requires an account.
  if (e.message.name === "PB_CONNECT") return e.next();
  if (!e.client.get("auth")) return;
  return e.next();
}

module.exports = {request, create, remove, realtime};
