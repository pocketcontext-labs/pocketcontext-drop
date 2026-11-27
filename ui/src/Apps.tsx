import OpenArrow from "./OpenArrow";
import { OpenPrivateApp } from "./PrivateApps";
import HelpTip from "./HelpTip";
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";
import { appURL } from "./AppAddress";
import CopyButton from "./CopyButton";
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
export default function Apps({ beta = false, official=false, developer=false, admin=false }: { beta?: boolean; official?:boolean; developer?: boolean; admin?: boolean }) {
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
    <section className="app-dashboard" aria-labelledby="apps-title">
      <div className="row heading">
        <h2 id="apps-title">Your apps</h2>
        {full ? (
          <span className="quiet">Quota reached</span>
        ) : (
          <div className="row">
            {developer && <a className="button secondary" href={viewHashes["private-new"]}>Create private app</a>}
            {!admin && <a className="button primary" href={official?viewHashes.catalog:beta ? viewHashes.deploy : viewHashes["app-new"]}>
            {official?"Create app":beta ? "Deploy an app" : "New app"}
          </a>}
          </div>
        )}
      </div>
      {quota && <p className="quiet">{quotaText(quota)} <HelpTip topic="app storage">This is the space set aside for your apps. Stopping an app keeps its files and still counts toward this total.</HelpTip></p>}
      {quota && !quota.unlimited && quota.limit > 0 && <progress aria-label="Storage used" value={quota.used} max={quota.limit} />}
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {apps === null ? (
        !error && <p className="quiet">Loading apps…</p>
      ) : apps.length === 0 ? (
        <p className="empty">
          {official ? "No apps yet." : "No apps yet. Create the first one."}
        </p>
      ) : (
        <ol className="apps" aria-label="Apps, newest first">
          {apps.map((app) => {
            const url = appURL(app.hostname);
            return <li className="dashboard-row" key={app.id}>
              <div>
                <div className="dashboard-identity">
                  <a className="dashboard-title" href={appHash(app.id)}>{app.name}</a>
                  <code className="slug">{app.slug}</code>
                </div>
                <div className="dashboard-meta">
                  <span className="dashboard-state">{app.archived ? "Archived" : stateLabels[app.desired_state]}</span>
                  {app.official_product && <><span className="dashboard-tier">{app.memory_mib / 256}×</span><span>{app.official_product === "crew" ? "Crew" : "Atlas"}</span></>}
                  {!app.official_product && app.kind === "crew" && <span>Crew · {app.crew_owner_email}</span>}
                  <time dateTime={app.created.replace(" ", "T")}>{formatDate(app.created)}</time>
                </div>
              </div>
              {app.developer_private
                ? <div className="dashboard-actions"><OpenPrivateApp app={app.id} hostname={app.hostname} disabled={!app.current_release || app.archived}/></div>
                : url && <div className="dashboard-actions">
                <CopyButton text={url} icon label="Copy app URL" />
                <a className="button secondary" href={url} target="_blank" rel="noreferrer">Open <OpenArrow /></a>
              </div>}
            </li>;
          })}
        </ol>
      )}
    </section>
  );
}
