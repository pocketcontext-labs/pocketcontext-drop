import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { formatDate } from "./Apps";
import CopyButton from "./CopyButton";
import { dropApi, type Invite, type Me, type Role, type User } from "./dropApi";
import { message } from "./errors";

const states: Record<Invite["state"], string> = {
  open: "Open",
  used: "Used",
  revoked: "Revoked",
  expired: "Expired",
};

function InviteForm({ onCreated }: { onCreated: () => void }) {
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<Role>("user");
  const [quota, setQuota] = useState(3);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [link, setLink] = useState<{ email: string; url: string } | null>(null);
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const created = await dropApi.createInvite({
        email: email.trim(),
        role,
        app_quota: quota,
      });
      setLink({ email: created.invite.email, url: created.sign_in_url });
      setEmail("");
      onCreated();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section aria-labelledby="invite-title">
      <h3 id="invite-title">Invite someone</h3>
      <form className="add" onSubmit={submit}>
        <label htmlFor="invite-email">Email</label>
        <input
          id="invite-email"
          type="email"
          required
          maxLength={254}
          autoComplete="off"
          value={email}
          onChange={(event) => setEmail(event.target.value)}
          placeholder="friend@example.com"
        />
        <div className="row">
          <label>
            Role
            <select
              value={role}
              onChange={(event) => setRole(event.target.value as Role)}
            >
              <option value="user">User</option>
              <option value="admin">Admin</option>
            </select>
          </label>
          <label>
            App quota
            <input
              type="number"
              min={0}
              max={100}
              value={quota}
              onChange={(event) => setQuota(Number(event.target.value))}
            />
          </label>
          <button className="primary" disabled={busy || !email.trim()}>
            Invite
          </button>
        </div>
        <p className="quiet">
          Drop sends no invite email: share the sign-in link yourself. It is
          valid for 7 days.
        </p>
      </form>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {link && (
        <div role="status" className="invite-link">
          <p>Sign-in link for {link.email}:</p>
          <div className="row">
            <code className="endpoint">{link.url}</code>
            <CopyButton text={link.url} />
          </div>
        </div>
      )}
    </section>
  );
}

function Invites({
  invites,
  onChanged,
}: {
  invites: Invite[];
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const revoke = async (invite: Invite) => {
    if (!window.confirm(`Revoke the invite for ${invite.email}?`)) return;
    setBusy(invite.id);
    setError("");
    try {
      await dropApi.revokeInvite(invite.id);
      onChanged();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy("");
    }
  };
  return (
    <section aria-labelledby="invites-title">
      <h3 id="invites-title">Invites</h3>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {invites.length === 0 ? (
        <p className="empty">No invites yet.</p>
      ) : (
        <ul className="apps" aria-label="Invites, newest first">
          {invites.map((invite) => (
            <li key={invite.id} className="row">
              <p>
                <span>{invite.email}</span>{" "}
                <span className="quiet">
                  {invite.role} · {states[invite.state]} · expires{" "}
                  {formatDate(invite.expires_at)}
                </span>
              </p>
              {invite.state === "open" && (
                <button
                  type="button"
                  className="secondary"
                  disabled={busy === invite.id}
                  onClick={() => void revoke(invite)}
                >
                  Revoke
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function Users({
  users,
  me,
  onChanged,
}: {
  users: User[];
  me: Me;
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const change = async (
    user: User,
    update: { role?: Role; app_quota?: number; suspended?: boolean },
  ) => {
    setBusy(user.id);
    setError("");
    try {
      await dropApi.setUser(user.id, update);
      onChanged();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy("");
    }
  };
  return (
    <section aria-labelledby="users-title">
      <h3 id="users-title">Accounts</h3>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      <ul className="apps" aria-label="Accounts, newest first">
        {users.map((user) => {
          // Nobody changes their own role or suspension, or the owner's.
          const locked = user.owner || user.id === me.id;
          return (
            <li key={user.id} className="account-controls">
              <p>
                <span>{user.email}</span>{" "}
                <span className="quiet">
                  {user.owner ? "owner · " : ""}
                  {user.suspended ? "suspended · " : ""}
                  {user.signed_in ? "signed in" : "not signed in yet"}
                </span>
              </p>
              <label>
                Role
                <select
                  value={user.role}
                  disabled={locked || busy === user.id}
                  onChange={(event) =>
                    void change(user, { role: event.target.value as Role })
                  }
                >
                  <option value="user">User</option>
                  <option value="admin">Admin</option>
                </select>
              </label>
              <label>
                Quota
                <input
                  type="number"
                  min={0}
                  max={100}
                  defaultValue={user.app_quota}
                  disabled={busy === user.id}
                  onBlur={(event) => {
                    const value = Number(event.target.value);
                    if (value !== user.app_quota)
                      void change(user, { app_quota: value });
                  }}
                />
              </label>
              <button
                type="button"
                className="secondary"
                disabled={locked || busy === user.id}
                onClick={() =>
                  window.confirm(user.suspended
                    ? `Restore sign-in and app management for ${user.email}?`
                    : `Suspend sign-in and app management for ${user.email}? Their deployed apps will keep serving.`) &&
                  void change(user, { suspended: !user.suspended })
                }
              >
                {user.suspended ? "Restore access" : "Suspend"}
              </button>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

/** Admins: accounts and invites. */
export default function AdminUsers({ me }: { me: Me }) {
  const [users, setUsers] = useState<User[] | null>(null);
  const [invites, setInvites] = useState<Invite[] | null>(null);
  const [error, setError] = useState("");
  const mounted = useRef(true);
  const load = useCallback(async () => {
    try {
      const [u, i] = await Promise.all([
        dropApi.listUsers(),
        dropApi.listInvites(),
      ]);
      if (!mounted.current) return;
      setUsers(u);
      setInvites(i);
      setError("");
    } catch (e) {
      if (mounted.current) setError(message(e));
    }
  }, []);
  useEffect(() => {
    mounted.current = true;
    void load();
    return () => {
      mounted.current = false;
    };
  }, [load]);
  return (
    <section className="panel" aria-labelledby="admin-users-title">
      <h2 id="admin-users-title">Users</h2>
      <InviteForm onCreated={() => void load()} />
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {invites && <Invites invites={invites} onChanged={() => void load()} />}
      {users ? (
        <Users users={users} me={me} onChanged={() => void load()} />
      ) : (
        !error && <p className="quiet">Loading accounts…</p>
      )}
    </section>
  );
}
