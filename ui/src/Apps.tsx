import RuntimeState from "./RuntimeState";
import OpenArrow from "./OpenArrow";
import { OpenPrivateApp } from "./PrivateApps";
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";
import { appURL } from "./AppAddress";
import CopyButton from "./CopyButton";
import { dropApi, type App } from "./dropApi";
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

export default function Apps({ beta = false, official=false, developer=false }: { beta?: boolean; official?:boolean; developer?: boolean }) {
  const [apps, setApps] = useState<App[] | null>(null);
  const [error, setError] = useState("");
  const mounted = useRef(true);
  const reads = useRef(0);
  const load = useCallback(async () => {
    const read = ++reads.current;
    try {
      const list = await dropApi.listApps();
      if (mounted.current && read === reads.current) {
        setApps(list.apps);
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
  return (
    <section className="app-dashboard" aria-labelledby="apps-title">
      <div className="row heading">
        <h2 id="apps-title">Your apps</h2>
          <div className="row">
            {developer && <a className="button secondary" href={viewHashes["private-new"]}>Create private app</a>}
            <a className="button primary" href={official || (beta && !developer) ? viewHashes.catalog : beta ? viewHashes.deploy : viewHashes["app-new"]}>
            {official || (beta && !developer) ? "Create app" : beta ? "Deploy an app" : "New app"}
          </a>
          </div>
      </div>
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
                  {app.archived ? <span className="dashboard-state">Archived</span> :
                    (official || app.desired_state === "on-request") ? <RuntimeState id={app.id} /> :
                    <span className="dashboard-state">{stateLabels[app.desired_state]}</span>}
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
