import {productName} from "./Official";
import RuntimeState from "./RuntimeState";
import OpenArrow from "./OpenArrow";
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

function CreateAppAction({ hosted, developer }: { hosted: boolean; developer: boolean }) {
  const [open, setOpen] = useState(false);
  const group = useRef<HTMLDivElement>(null);
  const toggle = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open) return;
    const dismiss = (event: PointerEvent) => {
      if (!group.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", dismiss);
    return () => document.removeEventListener("pointerdown", dismiss);
  }, [open]);

  return <div className="dashboard-create-actions" ref={group}
    onBlur={event => { if (!event.currentTarget.contains(event.relatedTarget)) setOpen(false); }}
    onKeyDown={event => {
      if (open && event.key === "Escape") {
        setOpen(false);
        toggle.current?.focus();
      }
    }}>
    <a className="button primary" href={hosted ? viewHashes.catalog : viewHashes["app-new"]}>
      {hosted ? "Create app" : "New app"}
    </a>
    {developer && <>
      <button className="button primary create-toggle" type="button" ref={toggle}
        aria-label="More app creation options" aria-expanded={open} aria-controls="create-options"
        onClick={() => setOpen(!open)}>
        <svg aria-hidden="true" width="16" height="16" viewBox="0 0 16 16">
          <path d="m4 6 4 4 4-4" fill="none" stroke="currentColor" strokeWidth="2" />
        </svg>
      </button>
      <div id="create-options" hidden={!open}>
        <a href={viewHashes["custom-new"]}>Create custom app</a>
      </div>
    </>}
  </div>;
}

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
      <div className="dashboard-heading">
        <h2 id="apps-title">Your apps</h2>
        <CreateAppAction developer={developer} hosted={beta || official || developer} />
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
            const product = app.official_product || (app.developer_private && app.executable_name);
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
                  {(app.official_product || app.private_tiered) && <span className="dashboard-tier">{app.memory_mib / 256}×</span>}
                  {product && <span>{productName(product)}</span>}
                  {app.developer_private && <span className="dashboard-tier">Custom</span>}
                  {!product && app.kind === "crew" && <span>Crew · {app.crew_owner_email}</span>}
                  <time dateTime={app.created.replace(" ", "T")}>{formatDate(app.created)}</time>
                </div>
              </div>
              {url && <div className="dashboard-actions">
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
