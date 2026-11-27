import { useEffect, useState } from "react";
import { dropApi, op, type Me, type Role } from "./dropApi";
import { message } from "./errors";
import { formatDate } from "./Apps";

type DirectoryUser = {
  owner: boolean;
  account: string;
  entry: string;
  invite: string;
  email: string;
  role: Role;
  status: string;
  workspaces: number;
  microcredits: number;
  joined: string;
};
const initialFilters = {
  search: "",
  status: "",
  role: "",
  workspaces: "",
  credits: "",
};
const pageSize = 25;
const statuses = [
  "waiting",
  "invited",
  "active",
  "suspended",
  "revoked",
  "expired",
];
export default function UserDirectory({
  me,
  waiting = false,
}: {
  me: Me;
  waiting?: boolean;
}) {
  const [filters, setFilters] = useState({
    ...initialFilters,
    status: waiting ? "waiting" : "",
  });
  const [offset, setOffset] = useState(0);
  const [results, setResults] = useState<{
    users: DirectoryUser[];
    total: number;
  }>();
  const [revision, setRevision] = useState(0);
  const [detail, setDetail] = useState<DirectoryUser>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [invite, setInvite] = useState(false);
  const [email, setEmail] = useState("");
  const [inviteRole, setInviteRole] = useState<Role>("user");
  const [link, setLink] = useState("");
  useEffect(() => {
    let active = true;
    setResults(undefined);
    setError("");
    const timer = setTimeout(
      () =>
        op<{ users: DirectoryUser[]; total: number }>("user.directory", {
          ...filters,
          offset,
          limit: pageSize,
        })
          .then((v) => {
            if (active) setResults(v);
          })
          .catch((e) => {
            if (active) setError(message(e));
          }),
      150,
    );
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [filters, offset, revision]);
  function filter(name: keyof typeof initialFilters, value: string) {
    setFilters((f) => ({ ...f, [name]: value }));
    setOffset(0);
    setDetail(undefined);
  }
  async function change(action: () => Promise<unknown>) {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      await action();
      setDetail(undefined);
      setRevision((r) => r + 1);
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  }
  const canManage =
    detail?.account && detail.account !== me.id && !detail.owner;
  return (
    <section className="panel wide official-page">
      <div className="row heading">
        <h2>Users</h2>
        <button className="secondary" onClick={() => setInvite(!invite)}>
          Invite someone
        </button>
      </div>
      {invite && (
        <form
          className="directory-filters"
          onSubmit={(e) => {
            e.preventDefault();
            void change(async () => {
              const r = await dropApi.createInvite({ email, role: inviteRole });
              setLink(r.sign_in_url);
              setInvite(false);
              setEmail("");
            });
          }}
        >
          <label>
            Email
            <input
              required
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          </label>
          <label>
            Invitation role
            <select
              value={inviteRole}
              onChange={(e) => setInviteRole(e.target.value as Role)}
            >
              <option value="user">User</option>
              <option value="admin">Admin</option>
            </select>
          </label>
          <button className="primary" disabled={busy}>
            Create invitation
          </button>
        </form>
      )}
      {link && (
        <p role="status">
          Share this sign-in link <a href={link}>{link}</a>
        </p>
      )}
      <div className="directory-filters">
        <label>
          Search users
          <input
            type="search"
            placeholder="Email or account ID"
            value={filters.search}
            onChange={(e) => filter("search", e.target.value)}
          />
        </label>
        {(
          [
            ["status", "Status", statuses],
            ["role", "Role", ["user", "admin"]],
            ["workspaces", "Has apps", ["yes", "no"]],
            ["credits", "Has credits", ["yes", "no"]],
          ] as const
        ).map(([key, label, options]) => (
          <label key={key}>
            {label}
            <select
              value={filters[key]}
              onChange={(e) => filter(key, e.target.value)}
            >
              <option value="">All</option>
              {options.map((o) => (
                <option key={o} value={o}>
                  {o[0].toUpperCase() + o.slice(1)}
                </option>
              ))}
            </select>
          </label>
        ))}
        <button
          className="secondary"
          onClick={() => {
            setFilters(initialFilters);
            setDetail(undefined);
            setOffset(0);
          }}
        >
          Clear filters
        </button>
      </div>
      {error && <p role="alert">{error}</p>}
      {detail && (
        <section className="directory-detail">
          <div className="row heading">
            <h3>{detail.email}</h3>
            <button className="link" onClick={() => setDetail(undefined)}>
              Close
            </button>
          </div>
          <p>
            {detail.status} · {detail.role}
            {detail.account && (
              <>
                {" "}
                · <code>{detail.account}</code>
              </>
            )}
          </p>
          {detail.entry &&
            ["waiting", "expired", "revoked"].includes(detail.status) && (
              <button
                className="primary"
                disabled={busy}
                onClick={() =>
                  void change(() =>
                    dropApi.changeWaitlist("invite", detail.entry),
                  )
                }
              >
                Approve and send invitation
              </button>
            )}
          {detail.entry && detail.status === "invited" && (
            <button
              className="secondary"
              disabled={busy}
              onClick={() =>
                void change(() =>
                  dropApi.changeWaitlist("resend", detail.entry),
                )
              }
            >
              Resend welcome email
            </button>
          )}
          {detail.status === "invited" && (
            <button
              className="secondary"
              disabled={busy}
              onClick={() =>
                window.confirm(
                  "Revoke the invitation for " + detail.email + "?",
                ) &&
                void change(() =>
                  detail.entry
                    ? dropApi.changeWaitlist("revoke", detail.entry)
                    : dropApi.revokeInvite(detail.invite),
                )
              }
            >
              Revoke invitation
            </button>
          )}
          {canManage && (
            <>
              <label>
                Account role
                <select
                  value={detail.role}
                  disabled={busy}
                  onChange={(e) =>
                    void change(() =>
                      dropApi.setUser(detail.account, {
                        role: e.target.value as Role,
                      }),
                    )
                  }
                >
                  <option value="user">User</option>
                  <option value="admin">Admin</option>
                </select>
              </label>
              <button
                className="secondary"
                disabled={busy}
                onClick={() =>
                  window.confirm(
                    "Change sign-in access for " + detail.email + "?",
                  ) &&
                  void change(() =>
                    dropApi.setUser(detail.account, {
                      suspended: detail.status !== "suspended",
                    }),
                  )
                }
              >
                {detail.status === "suspended"
                  ? "Restore access"
                  : "Suspend access"}
              </button>
            </>
          )}
        </section>
      )}
      {!results && !error && <p role="status">Loading users…</p>}
      {results && (
        <>
          <div className="row heading">
            <p>
              {results.users.length
                ? offset +
                  1 +
                  "–" +
                  (offset + results.users.length) +
                  " of " +
                  results.total.toLocaleString() +
                  " users"
                : "No matching users"}
            </p>
            <div>
              <button
                className="secondary"
                disabled={offset === 0}
                onClick={() => setOffset(offset - pageSize)}
              >
                Previous
              </button>{" "}
              <button
                className="secondary"
                disabled={offset + pageSize >= results.total}
                onClick={() => setOffset(offset + pageSize)}
              >
                Next
              </button>
            </div>
          </div>
          {!!results.users.length && (
            <div className="directory-scroll" tabIndex={0}>
              <table>
                <caption className="sr-only">User directory</caption>
                <thead>
                  <tr>
                    {[
                      "Account",
                      "Status",
                      "Role",
                      "Apps",
                      "Credits",
                      "Joined",
                    ].map((h) => (
                      <th key={h}>{h}</th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {results.users.map((r) => (
                    <tr key={r.email}>
                      <td>
                        <button className="link" onClick={() => setDetail(r)}>
                          {r.email}
                        </button>
                        <small>{r.account}</small>
                      </td>
                      <td>
                        <span className={"directory-state " + r.status}>
                          {r.status}
                        </span>
                      </td>
                      <td>{r.role}</td>
                      <td>{r.workspaces}</td>
                      <td>
                        {(r.microcredits / 1e6).toLocaleString(undefined, {
                          maximumFractionDigits: 2,
                        })}
                      </td>
                      <td>{formatDate(r.joined)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </section>
  );
}
