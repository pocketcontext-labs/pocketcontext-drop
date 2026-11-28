import { useCallback, useEffect, useRef, useState } from "react";
import { pb } from "./api";
import { op } from "./dropApi";
import { closeOnBackdropClick } from "./dialog";
import { appHash } from "./routes";
import "./backups.css";

interface Workspace {
  id: string;
  name: string;
  slug: string;
}
interface Backup {
  id: string;
  app: string;
  name: string;
  state: string;
  created: number;
  bytes: number;
  microcredits_per_gib_month?: number;
  ready_at?: number;
}
interface BackupList {
  eligible_apps: Workspace[];
  backups: Backup[];
  policies: { app: string; next_at: number }[];
  jobs: {
    id: string;
    backup: string;
    app: string;
    kind: string;
    state: string;
    error?: string;
  }[];
  pricing: {
    microcredits_per_gib_month: number;
    available_microcredits: number;
  };
}
const credits = (micro: number) =>
  (micro / 1e6).toLocaleString(undefined, { maximumFractionDigits: 6 });
const date = (seconds: number) => new Date(seconds * 1000).toLocaleString();
const size = (bytes: number) =>
  bytes < 1024 * 1024
    ? `${Math.ceil(bytes / 1024)} KiB`
    : `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
const message = (error: unknown) =>
  error instanceof Error ? error.message : "The request failed. Please retry.";

function failureReason(code?: string) {
  switch (code) {
    case "archive_too_large":
      return "Archive exceeds the 90 MiB backup limit.";
    case "guest_rejected":
      return "Crew could not complete the operation.";
    case "target_unavailable":
      return "The workspace was unavailable.";
    default:
      return "";
  }
}

function retentionEstimate(backup: Backup, rate: number) {
  let cursor = (backup.ready_at || backup.created) * 1000;
  const end = (backup.created + 604800) * 1000;
  let fraction = 0;
  while (cursor < end) {
    const now = new Date(cursor);
    const start = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1);
    const next = Date.UTC(now.getUTCFullYear(), now.getUTCMonth() + 1, 1);
    const boundary = Math.min(end, next);
    fraction += (boundary - cursor) / (next - start);
    cursor = boundary;
  }
  return credits((backup.bytes / 1024 ** 3) * rate * fraction);
}

function useBackups() {
  const [data, setData] = useState<BackupList | null>(null);
  const [error, setError] = useState("");
  const sequence = useRef(0);
  const refresh = useCallback(async () => {
    const request = ++sequence.current;
    try {
      const result = await op<BackupList>("backup.list");
      if (request === sequence.current) {
        setData(result);
        setError("");
      }
    } catch (e) {
      if (request === sequence.current) setError(message(e));
    }
  }, []);
  useEffect(() => {
    void refresh();
    return () => {
      sequence.current++;
    };
  }, [refresh]);
  return { data, error, refresh };
}

function BackupToggle({
  app,
  data,
  refresh,
}: {
  app: string;
  data: BackupList;
  refresh: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const policy = data.policies.find((p) => p.app === app);
  async function change(enabled: boolean) {
    setBusy(true);
    setError("");
    try {
      await op("backup.configure", { app, enabled });
      await refresh();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <label className="backup-toggle">
        <input
          type="checkbox"
          checked={!!policy}
          disabled={
            busy || (!policy && data.pricing.available_microcredits <= 0)
          }
          onChange={(event) => void change(event.target.checked)}
        />{" "}
        Daily backups
      </label>
      <p className="quiet">
        Copies are kept for seven days. Storage costs{" "}
        {credits(data.pricing.microcredits_per_gib_month)} credits per
        GiB-month.
      </p>
      {policy && (
        <p className="quiet">Next scheduled backup · {date(policy.next_at)}</p>
      )}
      {data.pricing.available_microcredits <= 0 && (
        <p>
          <a href="#/credits">Add credits</a> to create backups.
        </p>
      )}
      {error && <p role="alert">{error}</p>}
    </>
  );
}

export function AppBackupSetting({ app }: { app: string }) {
  const { data, refresh, error } = useBackups();
  if (!data?.eligible_apps?.some((a) => a.id === app))
    return error ? (
      <p>
        <a href="#/backups">Manage backups</a>
      </p>
    ) : null;
  return (
    <section className="app-backup-setting">
      <h3>Backups</h3>
      <BackupToggle key={app} app={app} data={data} refresh={refresh} />
      <a href={`#/backups?app=${encodeURIComponent(app)}`}>Manage backups</a>
    </section>
  );
}

async function download(backup: Backup) {
  const owner = pb.authStore.record?.id;
  const res = await fetch(
    `/api/drop/backups/${encodeURIComponent(backup.id)}/download`,
    {
      headers: { Authorization: `Bearer ${pb.authStore.token}` },
      credentials: "same-origin",
      redirect: "error",
    },
  );
  if (!res.ok)
    throw new Error(
      "The backup could not be downloaded. Refresh and try again.",
    );
  const blob = await res.blob();
  if (!owner || pb.authStore.record?.id !== owner)
    throw new Error("Sign in again to download your backup.");
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = `crew-backup-${backup.id}.zip`;
  document.body.append(link);
  link.click();
  link.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 30_000);
}

function RestoreDialog({
  backup,
  source,
  close,
  onQueued,
}: {
  backup: Backup;
  source?: Workspace;
  close: () => void;
  onQueued: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [target, setTarget] = useState("new");
  const [name, setName] = useState(backup.name + " restored");
  const [slug, setSlug] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const attempt = useRef({ input: "", key: "" });
  useEffect(() => {
    dialog.current?.showModal();
  }, []);
  const same = target === "same";
  async function restore() {
    setBusy(true);
    setError("");
    const input = same
      ? { backup: backup.id, same_app: slug }
      : { backup: backup.id, new_name: name.trim() };
    const serialized = JSON.stringify(input);
    if (attempt.current.input !== serialized)
      attempt.current = { input: serialized, key: crypto.randomUUID() };
    try {
      await op("backup.restore", input, attempt.current.key);
      onQueued();
      dialog.current?.close();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <dialog
      ref={dialog}
      aria-labelledby="backup-restore-title"
      onClose={close}
      onClick={busy ? undefined : closeOnBackdropClick}
      onCancel={(event) => {
        if (busy) event.preventDefault();
      }}
    >
      <h2 id="backup-restore-title">Restore {backup.name}</h2>
      <p className="quiet">Backup from {date(backup.created)}</p>
      <form
        className="backup-restore-form"
        onSubmit={(event) => {
          event.preventDefault();
          void restore();
        }}
      >
        <label>
          Destination
          <select
            value={target}
            onChange={(event) => setTarget(event.target.value)}
            disabled={busy}
          >
            <option value="new">New app</option>
            {source && <option value="same">Original app</option>}
          </select>
        </label>
        {same && source ? (
          <>
            <p>
              This replaces the current contents of {source.name} with this
              backup.
            </p>
            <label>
              Enter the app slug to confirm <code>{source.slug}</code>
              <input
                autoComplete="off"
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
                required
                disabled={busy}
              />
            </label>
          </>
        ) : (
          <label>
            New app name
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
              disabled={busy}
            />
          </label>
        )}
        {error && <p role="alert">{error}</p>}
        <div className="backup-actions">
          <button
            type="button"
            className="secondary"
            disabled={busy}
            onClick={() => dialog.current?.close()}
          >
            Cancel
          </button>
          <button
            type="submit"
            className="primary"
            disabled={busy || (same ? slug !== source?.slug : !name.trim())}
          >
            {same ? "Replace original app" : "Restore into new app"}
          </button>
        </div>
      </form>
    </dialog>
  );
}

export default function Backups({ initialApp = "" }: { initialApp?: string }) {
  const { data, error, refresh } = useBackups();
  const [selected, setSelected] = useState(initialApp);
  const [restore, setRestore] = useState<Backup | null>(null);
  const [busy, setBusy] = useState(false);
  const [feedback, setFeedback] = useState("");
  const [failure, setFailure] = useState("");
  const captureKey = useRef({ app: "", key: "" });
  const apps = data?.eligible_apps ?? [];
  const app = apps.find((a) => a.id === selected) ?? apps[0];
  const pending = !!data?.jobs.some((j) => j.state === "pending");
  useEffect(() => {
    if (!pending) return;
    const timer = window.setInterval(() => void refresh(), 3000);
    return () => window.clearInterval(timer);
  }, [pending, refresh]);
  async function capture() {
    if (!app) return;
    if (captureKey.current.app !== app.id)
      captureKey.current = { app: app.id, key: crypto.randomUUID() };
    setBusy(true);
    setFailure("");
    setFeedback("");
    try {
      await op("backup.create", { app: app.id }, captureKey.current.key);
      captureKey.current.app = "";
      setFeedback("Backup queued.");
      await refresh();
    } catch (e) {
      setFailure(message(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="backups-page">
      <h1>Backups</h1>
      {error && (
        <p role="alert">
          {error}{" "}
          <button className="secondary" onClick={() => void refresh()}>
            Retry
          </button>
        </p>
      )}
      {!data && !error && <p role="status">Loading backups…</p>}
      {data && (
        <>
          <section className="panel backup-panel">
            <h2>Backup settings</h2>
            {app ? (
              <>
                <label>
                  App
                  <select
                    value={app.id}
                    onChange={(e) => {
                      setSelected(e.target.value);
                      setFailure("");
                      setFeedback("");
                    }}
                  >
                    {apps.map((a) => (
                      <option key={a.id} value={a.id}>
                        {a.name} · {a.slug}
                      </option>
                    ))}
                  </select>
                </label>
                <BackupToggle
                  key={app.id}
                  app={app.id}
                  data={data}
                  refresh={refresh}
                />
                <button
                  className="secondary"
                  disabled={
                    busy ||
                    !data.policies.some((p) => p.app === app.id) ||
                    data.pricing.available_microcredits <= 0 ||
                    data.jobs.some(
                      (j) => j.app === app.id && j.state === "pending",
                    )
                  }
                  onClick={() => void capture()}
                >
                  Back up now
                </button>
              </>
            ) : (
              <p>
                Backups are available for Crew apps running a release with
                backup support.
              </p>
            )}
          </section>
          <section className="panel backup-panel">
            <h2>Saved backups</h2>
            {data.backups.length === 0 && (
              <p className="quiet">No backups yet.</p>
            )}
            {data.backups.map((b) => (
              <article className="backup-record" key={b.id}>
                <div>
                  <strong>{b.name}</strong>
                  <p className="quiet">
                    {date(b.created)} · {b.state}
                    {b.state === "ready" && ` · ${size(b.bytes)}`}
                  </p>
                  {b.state === "failed" && (
                    <p>
                      {failureReason(
                        data.jobs.find(
                          (j) => j.kind === "capture" && j.backup === b.id,
                        )?.error,
                      )}
                    </p>
                  )}
                  {b.state === "ready" && (
                    <p className="quiet">
                      Expires {date(b.created + 604800)}
                      {b.microcredits_per_gib_month !== undefined && (
                        <>
                          {" "}
                          · Estimated storage through expiry ·{" "}
                          {retentionEstimate(
                            b,
                            b.microcredits_per_gib_month,
                          )}{" "}
                          credits
                        </>
                      )}
                    </p>
                  )}
                </div>
                {b.state === "ready" && (
                  <div className="backup-actions">
                    <button
                      className="secondary"
                      disabled={busy}
                      onClick={async () => {
                        setBusy(true);
                        setFailure("");
                        try {
                          await download(b);
                        } catch (e) {
                          setFailure(message(e));
                        } finally {
                          setBusy(false);
                        }
                      }}
                    >
                      Download ZIP
                    </button>
                    <button className="secondary" onClick={() => setRestore(b)}>
                      Restore
                    </button>
                  </div>
                )}
              </article>
            ))}
          </section>
          {data.jobs.some((j) => j.kind === "restore") && (
            <section className="panel backup-panel">
              <h2>Restores</h2>
              {data.jobs
                .filter((j) => j.kind === "restore")
                .map((j) => (
                  <div className="backup-record" key={j.id}>
                    <span>
                      {data.backups.find((b) => b.id === j.backup)?.name ||
                        "Crew backup"}{" "}
                      · {j.state} {failureReason(j.error)}
                    </span>
                    <a href={appHash(j.app)}>View destination app</a>
                  </div>
                ))}
            </section>
          )}
        </>
      )}
      {feedback && <p role="status">{feedback}</p>}
      {failure && <p role="alert">{failure}</p>}
      {restore && (
        <RestoreDialog
          backup={restore}
          source={apps.find((a) => a.id === restore.app)}
          close={() => setRestore(null)}
          onQueued={() => {
            setFeedback("Restore queued.");
            setFailure("");
            void refresh();
          }}
        />
      )}
    </div>
  );
}
