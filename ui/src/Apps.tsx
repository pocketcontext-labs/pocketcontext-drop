import HelpTip from "./HelpTip";
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";
import AppAddress from "./AppAddress";
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

export function quotaText(quota: Quota): string {
  const gib = (mib: number) => (mib / 1024).toLocaleString(undefined, {maximumFractionDigits: 2});
  return quota.unlimited ? `${gib(quota.used)} GiB retained` : `${gib(quota.used)} of ${gib(quota.limit)} GiB retained`;
}

/** The account's apps, newest first, with its quota. */
export default function Apps({ beta = false, official=false }: { beta?: boolean; official?:boolean }) {
  const [apps, setApps] = useState<App[] | null>(null);
  const [quota, setQuota] = useState<Quota | null>(null);
  const [error, setError] = useState("");
  const mounted = useRef(true);
  const reads = useRef(0);
  const load = useCallback(async () => {
    const read = ++reads.current;
    try {
      const list = await dropApi.listApps();
      if (mounted.current && read === reads.current) {
        setApps(list.apps);
        setQuota(list.quota);
        setError("");
      }
    } catch (e) {
      if (mounted.current && read === reads.current) setError(message(e));
    }
  }, []);
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
    <section className={"panel wide" + (official ? " workspace-list" : "")} aria-labelledby="apps-title">
      <div className="row heading">
        <h2 id="apps-title">{official?"Your workspaces":"Your apps"}</h2>
        {full ? (
          <span className="quiet">Quota reached</span>
        ) : (
          <a className="button primary" href={official?viewHashes.catalog:beta ? viewHashes.deploy : viewHashes["app-new"]}>
            {official?"Create workspace":beta ? "Deploy an app" : "New app"}
          </a>
        )}
      </div>
      {quota && <p className="quiet">{quotaText(quota)} <HelpTip topic="workspace storage">This is the space set aside for your workspaces. Stopping a workspace keeps its files and still counts toward this total.</HelpTip></p>}
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {apps === null ? (
        !error && <p className="quiet">Loading apps…</p>
      ) : apps.length === 0 ? (
        <p className="empty">
          {official ? "No workspaces yet." : "No apps yet. Create the first one."}
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
                {app.official_product&&<> · {app.official_product==="crew"?"Crew":"Atlas"} · {app.memory_mib/256}×</>}
                {!app.official_product&&app.kind === "crew" && <> · Crew · {app.crew_owner_email}</>}
                {" · "}
                <time dateTime={app.created.replace(" ", "T")}>
                  {formatDate(app.created)}
                </time>
              </p>
              <AppAddress hostname={app.hostname} />
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
