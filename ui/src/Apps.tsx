import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";
import { dropApi, type App, type Quota } from "./dropApi";
import { message } from "./errors";
import { appHash, viewHashes } from "./routes";

const dateFormat = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
});
export function formatDate(value: string): string {
  // PocketBase stores "YYYY-MM-DD HH:MM:SS.sssZ" in UTC.
  const date = new Date(value.replace(" ", "T"));
  return Number.isNaN(date.getTime()) ? value : dateFormat.format(date);
}

export const stateLabels: Record<App["desired_state"], string> = {
  running: "Running",
  "on-request": "On request",
  stopped: "Stopped",
};

/** "2 of 3 apps", or "4 apps" for an admin (no quota). */
export function quotaText(quota: Quota): string {
  const apps = (n: number) => `${n} ${n === 1 ? "app" : "apps"}`;
  return quota.unlimited
    ? `${apps(quota.used)} (admins have no quota)`
    : `${quota.used} of ${apps(quota.limit)}`;
}

/** The account's apps, newest first, with its quota. */
export default function Apps() {
  const [apps, setApps] = useState<App[] | null>(null);
  const [quota, setQuota] = useState<Quota | null>(null);
  const [archived, setArchived] = useState(false);
  const [error, setError] = useState("");
  const mounted = useRef(true);
  const reads = useRef(0);
  const load = useCallback(async () => {
    const read = ++reads.current;
    try {
      const list = await dropApi.listApps(archived);
      if (mounted.current && read === reads.current) {
        setApps(list.apps);
        setQuota(list.quota);
        setError("");
      }
    } catch (e) {
      if (mounted.current && read === reads.current) setError(message(e));
    }
  }, [archived]);
  useEffect(() => {
    mounted.current = true;
    void load();
    let stop: (() => void) | undefined;
    // Changes from the CLI, MCP or another tab appear without a reload.
    api
      .watchApps(() => void load())
      .then((unsubscribe) => {
        if (mounted.current) stop = unsubscribe;
        else unsubscribe();
      })
      .catch(() => {});
    return () => {
      mounted.current = false;
      stop?.();
    };
  }, [load]);
  const full = quota !== null && !quota.unlimited && quota.used >= quota.limit;
  return (
    <section className="panel wide" aria-labelledby="apps-title">
      <div className="row heading">
        <h2 id="apps-title">Your apps</h2>
        {full ? (
          <span className="quiet">Quota reached</span>
        ) : (
          <a className="button primary" href={viewHashes["app-new"]}>
            New app
          </a>
        )}
      </div>
      {quota && <p className="quiet">{quotaText(quota)}</p>}
      <label className="check">
        <input
          type="checkbox"
          checked={archived}
          onChange={(event) => {
            setApps(null);
            setArchived(event.target.checked);
          }}
        />
        Show archived apps
      </label>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {apps === null ? (
        !error && <p className="quiet">Loading apps…</p>
      ) : apps.length === 0 ? (
        <p className="empty">
          {archived ? "No archived apps." : "No apps yet. Create the first one."}
        </p>
      ) : (
        <ol className="apps" aria-label="Apps, newest first">
          {apps.map((app) => (
            <li key={app.id}>
              <p>
                <a href={appHash(app.id)}>{app.name}</a>{" "}
                <code className="slug">{app.slug}</code>
              </p>
              <p className="quiet">
                {app.archived ? "Archived" : stateLabels[app.desired_state]}
                {app.kind === "crew" && <> · Crew · {app.crew_owner_email}</>}
                {app.hostname && <> · {app.hostname}</>} ·{" "}
                <time dateTime={app.created.replace(" ", "T")}>
                  {formatDate(app.created)}
                </time>
              </p>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
