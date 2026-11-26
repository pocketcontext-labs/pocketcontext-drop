import { useCallback, useEffect, useRef, useState } from "react";
import { formatDate } from "./Apps";
import { dropApi, type WaitlistEntry } from "./dropApi";
import { message } from "./errors";

const pageSize = 50;
function accountState(entry: WaitlistEntry) {
  if (entry.state === "used") return entry.suspended ? "Suspended" : "Active";
  return entry.state === "open" ? "Invited" : "Waiting";
}

type Action = "invite" | "resend" | "revoke" | "suspend" | "restore";
type Pending = { entry: WaitlistEntry; action: Action };

export default function AdminWaitlist({ account }: { account: string }) {
  const [query, setQuery] = useState("");
  const [search, setSearch] = useState("");
  const [offset, setOffset] = useState(0);
  const [entries, setEntries] = useState<WaitlistEntry[] | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const [error, setError] = useState("");
  const [success, setSuccess] = useState("");
  const [pending, setPending] = useState<Pending | null>(null);
  const [busy, setBusy] = useState(false);
  const reads = useRef(0);
  const load = useCallback(async () => {
    const read = ++reads.current;
    try {
      const result = await dropApi.listWaitlist(search, offset, pageSize);
      if (read !== reads.current) return;
      setEntries(result.entries);
      setHasMore(result.has_more);
      setError("");
    } catch (e) {
      if (read === reads.current) setError(message(e));
    }
  }, [search, offset]);
  useEffect(() => {
    setEntries(null);
    setHasMore(false);
    setPending(null);
    void load();
    return () => {
      reads.current++;
    };
  }, [load]);

  useEffect(() => {
    if (
      !entries?.some(
        (entry) => entry.state === "open" && entry.mail_state === "queued",
      )
    )
      return;
    const timer = setTimeout(() => void load(), 5000);
    return () => clearTimeout(timer);
  }, [entries, load]);

  async function change({ entry, action }: Pending) {
    if (busy) return;
    setBusy(true);
    setError("");
    setSuccess("");
    try {
      if (action === "suspend" || action === "restore") {
        await dropApi.setUser(entry.account_id, {
          suspended: action === "suspend",
        });
      } else {
        await dropApi.changeWaitlist(action, entry.id);
      }
      const labels: Record<Action, string> = {
        invite: "Invitation queued",
        resend: "Invitation queued again",
        revoke: "Invitation revoked",
        suspend: "Access suspended",
        restore: "Access restored",
      };
      setSuccess(labels[action] + " for " + entry.email + ".");
      setPending(null);
      await load();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="panel wide" aria-labelledby="waitlist-title">
      <div className="row heading">
        <h2 id="waitlist-title">Waitlist</h2>
        <button
          className="secondary"
          disabled={busy}
          onClick={() => void load()}
        >
          Refresh
        </button>
      </div>
      <form
        className="waitlist-search"
        role="search"
        onSubmit={(event) => {
          event.preventDefault();
          setOffset(0);
          setSearch(query.trim());
          setSuccess("");
          setPending(null);
        }}
      >
        <label>
          Search by email
          <input
            type="search"
            value={query}
            maxLength={254}
            onChange={(e) => setQuery(e.target.value)}
          />
        </label>
        <button className="secondary" disabled={busy}>
          Search
        </button>
        {search && (
          <button
            type="button"
            className="link"
            disabled={busy}
            onClick={() => {
              setQuery("");
              setSearch("");
              setOffset(0);
              setSuccess("");
            }}
          >
            Clear
          </button>
        )}
      </form>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {success && <p role="status">{success}</p>}
      {entries === null ? (
        !error && <p role="status">Loading waitlist…</p>
      ) : entries.length === 0 ? (
        <p>
          {search
            ? "No verified addresses match this search."
            : "No verified waitlist entries."}
        </p>
      ) : (
        <ol
          className="apps waitlist-entries"
          aria-label="Verified waitlist entries, oldest first"
        >
          {entries.map((entry) => {
            const state = accountState(entry);
            const confirming = pending?.entry.id === entry.id ? pending : null;
            return (
              <li key={entry.id}>
                <div className="row heading">
                  <strong>{entry.email}</strong>
                  <span className="badge">{state}</span>
                </div>
                <p className="quiet">
                  Verified {formatDate(entry.verified_at)}
                  {entry.invited_at && <> · Invited {formatDate(entry.invited_at)}</>}
                  {entry.state === "open" && entry.expires_at && <> · Expires {formatDate(entry.expires_at)}</>}
                  {entry.activated_at && <> · Activated {formatDate(entry.activated_at)}</>}
                  {entry.state === "revoked" && " · Invitation revoked"}
                  {entry.state === "expired" && " · Invitation expired"}
                  {entry.state === "open" &&
                    entry.mail_state &&
                    " · " +
                      {
                        sent: "Welcome email sent",
                        queued: "Welcome email queued",
                        failed: "Welcome email failed",
                      }[entry.mail_state]}
                </p>
                {confirming ? (
                  <div
                    className="waitlist-confirm"
                    role="group"
                    aria-label={"Confirm " + confirming.action}
                  >
                    <p>
                      {confirming.action === "suspend" ? (
                        <>
                          Suspend sign-in and app management for{" "}
                          <strong>{entry.email}</strong>? Their deployed apps
                          will keep serving.
                        </>
                      ) : confirming.action === "restore" ? (
                        <>
                          Restore sign-in and app management for{" "}
                          <strong>{entry.email}</strong>?
                        </>
                      ) : (
                        <>
                          Revoke the open invitation for{" "}
                          <strong>{entry.email}</strong>? They can stay on the
                          waitlist.
                        </>
                      )}
                    </p>
                    <div className="waitlist-actions">
                      <button
                        className="primary"
                        disabled={busy}
                        onClick={() => void change(confirming)}
                      >
                        Confirm {confirming.action}
                      </button>
                      <button
                        className="secondary"
                        disabled={busy}
                        onClick={() => setPending(null)}
                      >
                        Cancel
                      </button>
                    </div>
                  </div>
                ) : (
                  <div className="waitlist-actions">
                    {state === "Waiting" && (
                      <button
                        className="primary"
                        disabled={busy}
                        onClick={() => void change({ entry, action: "invite" })}
                      >
                        Invite and send welcome email
                      </button>
                    )}
                    {state === "Invited" && (
                      <>
                        <button
                          className="secondary"
                          disabled={busy}
                          onClick={() =>
                            void change({ entry, action: "resend" })
                          }
                        >
                          Resend welcome email
                        </button>
                        <button
                          className="secondary"
                          disabled={busy}
                          onClick={() =>
                            setPending({ entry, action: "revoke" })
                          }
                        >
                          Revoke invitation
                        </button>
                      </>
                    )}
                    {entry.state === "used" && entry.account_id && entry.account_id !== account && (
                      <button
                        className="secondary"
                        disabled={busy}
                        onClick={() =>
                          setPending({
                            entry,
                            action: entry.suspended ? "restore" : "suspend",
                          })
                        }
                      >
                        {entry.suspended ? "Restore access" : "Suspend access"}
                      </button>
                    )}
                  </div>
                )}
              </li>
            );
          })}
        </ol>
      )}
      {(offset > 0 || hasMore) && (
        <nav aria-label="Waitlist pages" className="waitlist-actions">
          <button
            className="secondary"
            disabled={busy || !offset}
            onClick={() => setOffset(Math.max(0, offset - pageSize))}
          >
            Previous
          </button>
          <button
            className="secondary"
            disabled={busy || !hasMore}
            onClick={() => setOffset(offset + pageSize)}
          >
            Next
          </button>
        </nav>
      )}
    </section>
  );
}
