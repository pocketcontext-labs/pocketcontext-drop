import { DeleteApp, DeletionStatus } from "./AppDeletion";
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { api, NAME_LIMIT, validName } from "./api";
import { NumberInput, type NumberField } from "./AppNew";
import { formatDate, stateLabels } from "./Apps";
import {
  dropApi,
  OpError,
  type App,
  type AppSettings,
  type AppStatus,
  type DesiredState,
  type HealthKind,
  type Secret,
} from "./dropApi";
import { message } from "./errors";
import AppAddress from "./AppAddress";
import Releases from "./Releases";
import BetaDeploy, { BetaStatus } from "./BetaDeploy";
import UsagePanel from "./Usage";
import { appHash, viewHashes, type AppTab } from "./routes";

const tabs: { tab: AppTab; label: string }[] = [
  { tab: "overview", label: "Overview" },
  { tab: "releases", label: "Releases" },
  { tab: "settings", label: "Settings" },
  { tab: "env", label: "Env & secrets" },
  { tab: "usage", label: "Usage" },
];

/** An environment or secret name (internal/appspec). */
const ENV_NAME = /^[A-Z][A-Z0-9_]{0,63}$/;
export function envNameProblem(name: string): string {
  if (!ENV_NAME.test(name))
    return "Names are A-Z, 0-9 and _, starting with a letter (at most 64).";
  if (
    name === "HOME" ||
    name === "PATH" ||
    name.startsWith("NIBRUN_") ||
    name.startsWith("DROP_")
  )
    return `${name} is reserved.`;
  return "";
}

/** People's names of the runtime phases (internal/dropapi Status). */
export const phaseLabels: Record<string, string> = {
  archived: "Archived",
  no_release: "No release",
  unplaced: "Not placed",
  pending: "Pending",
  staging: "Staging",
  staging_failed: "Staging failed",
  secret_failed: "Secrets failed",
  deploying: "Deploying",
  starting: "Starting",
  running: "Running",
  sleeping: "Sleeping",
  unhealthy: "Unhealthy",
  stopped: "Stopped",
  failed: "Failed",
  unknown: "Unknown",
};

/** How often the Overview refreshes the phase. */
const PHASE_REFRESH_MS = 15000;

/** The app's runtime phase, refreshed while the Overview is open. */
function Phase({ app }: { app: App }) {
  const [status, setStatus] = useState<AppStatus | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let active = true;
    const load = async () => {
      try {
        const s = await dropApi.appStatus(app.id);
        if (active) {
          setStatus(s);
          setFailed(false);
        }
      } catch {
        if (active) setFailed(true);
      }
    };
    void load();
    const timer = window.setInterval(() => void load(), PHASE_REFRESH_MS);
    return () => {
      active = false;
      window.clearInterval(timer);
    };
    // Reload at once when the app changes (start, stop, a new deployment).
  }, [app.id, app.updated, app.deployment_id, app.desired_state]);
  if (!status)
    return (
      <>
        <dt>Phase</dt>
        <dd className="quiet">{failed ? "Not available" : "Loading…"}</dd>
      </>
    );
  return (
    <>
      <dt>Phase</dt>
      <dd>
        <span className={`phase phase-${status.phase}`}>
          {phaseLabels[status.phase] ?? status.phase}
        </span>
        {status.message && (
          <span className="quiet"> · {status.message}</span>
        )}
        {status.restart_count > 0 && (
          <span className="quiet">
            {" "}
            · {status.restart_count} restarts
            {status.last_exit_code !== null
              ? `, last exit code ${status.last_exit_code}`
              : ""}
          </span>
        )}
        {status.node && <span className="quiet"> · node {status.node}</span>}
      </dd>
      {status.last_hook && <>
        <dt>Last lifecycle hook</dt>
        <dd>{status.last_hook.event} · {status.last_hook.outcome} · <time dateTime={status.last_hook.time}>{formatDate(status.last_hook.time)}</time></dd>
      </>}
    </>
  );
}

function Overview({
  app,
  onChange,
}: {
  app: App;
  onChange: (app: App) => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const act = async (
    action: "app.start" | "app.stop" | "app.archive" | "app.restore",
  ) => {
    if (
      action === "app.archive" &&
      !window.confirm(
        `Archive ${app.name}? It stops running; its data, secrets and slug stay.`,
      )
    )
      return;
    setBusy(true);
    setError("");
    try {
      onChange(await dropApi.appAction(action, app.id));
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div>
      <dl className="facts">
        <dt>State</dt>
        <dd>{app.archived ? "Archived" : stateLabels[app.desired_state]}</dd>
        <Phase app={app} />
        {app.kind === "crew" && <>
          <dt>App type</dt><dd>PocketContext Crew</dd>
          <dt>Sign-in recipient</dt><dd>{app.crew_owner_email}</dd>
          <dt>Email delivery</dt><dd>{app.sign_in_email_enabled ? "Enabled" : "Disabled"}. Delivery pauses while the app is stopped.</dd>
        </>}
        <dt>App URL</dt>
        <dd>
          {app.hostname ? (
            <AppAddress hostname={app.hostname} />
          ) : (
            <span className="quiet">None on this server</span>
          )}
        </dd>
        <dt>Slug</dt>
        <dd>
          <code>{app.slug}</code>
        </dd>
        <dt>Deployment</dt>
        <dd>
          <code>{app.deployment_id}</code>
        </dd>
        <dt>Created</dt>
        <dd>{formatDate(app.created)}</dd>
      </dl>
      <p className="quiet">
        {app.current_release ? (
          <>The current release is set in <a href={appHash(app.id, "releases")}>Releases</a>.</>
        ) : (
          <>To add the first release, upload an app file in <a href={appHash(app.id, "releases")}>Releases</a>.</>
        )}{" "}
        An admin places the app on a node; until then nothing runs.
      </p>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      <div className="row">
        {app.archived ? (
          <button
            type="button"
            className="primary"
            disabled={busy}
            onClick={() => void act("app.restore")}
          >
            Restore
          </button>
        ) : (
          <>
            {app.desired_state === "stopped" ? (
              <button
                type="button"
                className="primary"
                disabled={busy}
                onClick={() => void act("app.start")}
              >
                Start
              </button>
            ) : (
              <button
                type="button"
                className="secondary"
                disabled={busy}
                onClick={() => void act("app.stop")}
              >
                Stop
              </button>
            )}
            <button
              type="button"
              className="secondary"
              disabled={busy}
              onClick={() => void act("app.archive")}
            >
              Archive
            </button>
          </>
        )}
        <DeleteApp app={app} />
      </div>
    </div>
  );
}

/** The settings form: only changed fields are sent. */
function Settings({
  app,
  onChange,
}: {
  app: App;
  onChange: (app: App) => void;
}) {
  const [name, setName] = useState(app.name);
  const [state, setState] = useState<DesiredState>(app.desired_state);
  const [emailEnabled, setEmailEnabled] = useState(Boolean(app.sign_in_email_enabled));
  const [syncInterval, setSyncInterval] = useState(app.replica_sync_interval ?? "");
  const [executable, setExecutable] = useState(app.executable_name);
  const [args, setArgs] = useState(app.args.join("\n"));
  const [health, setHealth] = useState<HealthKind>(app.health_kind);
  const [healthPath, setHealthPath] = useState(app.health_path || "/");
  const [numbers, setNumbers] = useState<Record<NumberField, number>>({
    http_port: app.http_port,
    hook_port: app.hook_port,
    vcpu: app.vcpu,
    memory_mib: app.memory_mib,
    disk_mib: app.disk_mib,
    idle_timeout_ms: app.idle_timeout_ms,
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [status, setStatus] = useState("");
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setError("");
    setStatus("");
    const change: AppSettings & { name?: string; sign_in_email_enabled?: boolean; replica_sync_interval?: string } = {};
    try {
      const trimmed = validName(name);
      if (trimmed !== app.name) change.name = trimmed;
    } catch (e) {
      setError(message(e));
      return;
    }
    if (state !== app.desired_state) change.desired_state = state;
    if (app.kind === "crew" && emailEnabled !== Boolean(app.sign_in_email_enabled))
      change.sign_in_email_enabled = emailEnabled;
    if (app.kind === "crew" && syncInterval.trim() !== "" && syncInterval.trim() !== (app.replica_sync_interval ?? ""))
      change.replica_sync_interval = syncInterval.trim();
    if (executable.trim() !== app.executable_name)
      change.executable_name = executable.trim();
    const list = args.split("\n").filter((line) => line !== "");
    if (list.join("\n") !== app.args.join("\n")) change.args = list;
    if (health !== app.health_kind) change.health_kind = health;
    const path = health === "http" ? healthPath.trim() : "";
    if (path !== app.health_path) change.health_path = path;
    for (const field of Object.keys(numbers) as NumberField[])
      if (numbers[field] !== app[field]) change[field] = numbers[field];
    if (Object.keys(change).length === 0) {
      setStatus("Nothing changed.");
      return;
    }
    setBusy(true);
    try {
      const result = await dropApi.setApp(app.id, change);
      onChange(result.app);
      setStatus(
        result.deployment_bumped
          ? `Saved. New deployment ${result.app.deployment_id}.`
          : "Saved.",
      );
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const number = (field: NumberField) => (
    <NumberInput
      field={field}
      value={numbers[field]}
      disabled={app.archived || (field === "disk_mib" && app.disk_locked)}
      onChange={(value) => setNumbers((n) => ({ ...n, [field]: value }))}
    />
  );
  return (
    <form className="add" onSubmit={submit}>
      <fieldset disabled={app.archived || busy}>
        <label>
          App name
          <input
            name="name"
            value={name}
            maxLength={NAME_LIMIT}
            required
            onChange={(event) => setName(event.target.value)}
          />
        </label>
        <div className="row">
          <label>
            State
            <select
              value={state}
              onChange={(event) => setState(event.target.value as DesiredState)}
            >
              <option value="on-request">On request (sleeps when idle)</option>
              <option value="running">Running</option>
              <option value="stopped">Stopped</option>
            </select>
          </label>
          {number("http_port")}
          {number("hook_port")}
          <label>
            Executable
            <input
              name="executable"
              value={executable}
              maxLength={32}
              required
              autoCapitalize="none"
              spellCheck={false}
              onChange={(event) => setExecutable(event.target.value)}
            />
          </label>
        </div>
        <div className="row">
          {number("vcpu")}
          {number("memory_mib")}
          {number("disk_mib")}
          {number("idle_timeout_ms")}
        </div>
        <div className="row">
          <label>
            Health check
            <select
              value={health}
              onChange={(event) => setHealth(event.target.value as HealthKind)}
            >
              <option value="tcp">TCP port open</option>
              <option value="http">HTTP path</option>
            </select>
          </label>
          {health === "http" && (
            <label>
              Health path
              <input
                name="health_path"
                value={healthPath}
                maxLength={1024}
                required
                onChange={(event) => setHealthPath(event.target.value)}
              />
            </label>
          )}
        </div>
        {app.kind === "crew" && (
          <label>
            <input type="checkbox" checked={emailEnabled} onChange={(event) => setEmailEnabled(event.target.checked)} />
            Deliver sign-in codes to {app.crew_owner_email}
          </label>
        )}
        {app.kind === "crew" && (
          <label>
            Replica sync interval (1s to 300s)
            <input
              name="replica_sync_interval"
              value={syncInterval}
              placeholder="10s"
              maxLength={8}
              autoCapitalize="none"
              spellCheck={false}
              onChange={(event) => setSyncInterval(event.target.value)}
            />
          </label>
        )}
        <label>
          Arguments (one per line)
          <textarea
            name="args"
            rows={3}
            value={args}
            spellCheck={false}
            onChange={(event) => setArgs(event.target.value)}
          />
        </label>
        <p className="quiet">
          A change to what the app runs with (everything but the name, the
          state and the disk) starts a new deployment, which restarts it.
        </p>
      </fieldset>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      <p role="status" className="quiet">
        {status}
      </p>
      <div className="row">
        <button className="primary" disabled={busy || app.archived}>
          Save settings
        </button>
      </div>
    </form>
  );
}

function EnvVars({
  app,
  secrets,
  onChange,
}: {
  app: App;
  secrets: Secret[];
  onChange: (app: App) => void;
}) {
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const change = async (env: { set?: Record<string, string>; unset?: string[] }) => {
    setBusy(true);
    setError("");
    try {
      onChange((await dropApi.setApp(app.id, { env })).app);
      return true;
    } catch (e) {
      setError(message(e));
      return false;
    } finally {
      setBusy(false);
    }
  };
  const add = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const problem =
      envNameProblem(name) ||
      (secrets.some((s) => s.name === name)
        ? `${name} is a secret of this app.`
        : "");
    if (problem) {
      setError(problem);
      return;
    }
    if (await change({ set: { [name]: value } })) {
      setName("");
      setValue("");
    }
  };
  const names = Object.keys(app.env).sort();
  return (
    <section aria-labelledby="env-title">
      <h3 id="env-title">Environment variables</h3>
      <p className="quiet">
        Plain values, visible to you and admins. Use a secret for passwords and
        keys. Values may use {"${NIBRUN_HOSTNAME}"} and {"${NIBRUN_HTTP_PORT}"}.
      </p>
      {names.length === 0 ? (
        <p className="empty">No environment variables.</p>
      ) : (
        <ul className="apps" aria-label="Environment variables">
          {names.map((key) => (
            <li key={key} className="row">
              <p>
                <code>{key}</code> = <code>{app.env[key]}</code>
              </p>
              <button
                type="button"
                className="secondary"
                disabled={busy || app.archived}
                onClick={() => void change({ unset: [key] })}
              >
                Remove
              </button>
            </li>
          ))}
        </ul>
      )}
      <form className="add" onSubmit={add} aria-label="Add a variable">
        <fieldset disabled={busy || app.archived}>
          <div className="row">
            <label>
              Variable name
              <input
                name="env_name"
                value={name}
                maxLength={64}
                required
                autoCapitalize="characters"
                spellCheck={false}
                onChange={(event) => setName(event.target.value.trim())}
                placeholder="WELCOME_TEXT"
              />
            </label>
            <label>
              Variable value
              <input
                name="env_value"
                value={value}
                maxLength={4096}
                spellCheck={false}
                onChange={(event) => setValue(event.target.value)}
              />
            </label>
            <button className="secondary">Set variable</button>
          </div>
        </fieldset>
      </form>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
    </section>
  );
}

function Secrets({
  app,
  secrets,
  onChange,
}: {
  app: App;
  secrets: Secret[];
  onChange: () => void;
}) {
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [status, setStatus] = useState("");
  const valueField = useRef<HTMLInputElement>(null);
  const set = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setError("");
    setStatus("");
    const problem =
      envNameProblem(name) ||
      (name in app.env ? `${name} is a plain variable of this app.` : "");
    if (problem) {
      setError(problem);
      return;
    }
    setBusy(true);
    try {
      const result = await dropApi.setSecret(app.id, name, value);
      setStatus(
        `${result.secret.name} saved (version ${result.secret.version}). New deployment ${result.deployment_id}.`,
      );
      setName("");
      onChange();
    } catch (e) {
      setError(message(e));
    } finally {
      // The value never stays in the page.
      setValue("");
      setBusy(false);
    }
  };
  const remove = async (secret: Secret) => {
    if (!window.confirm(`Delete the secret ${secret.name}? Every version goes.`))
      return;
    setBusy(true);
    setError("");
    setStatus("");
    try {
      await dropApi.deleteSecret(app.id, secret.name);
      setStatus(`${secret.name} deleted.`);
      onChange();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section aria-labelledby="secrets-title">
      <h3 id="secrets-title">Secrets</h3>
      <p className="quiet">
        Stored in AWS SSM and given to the app as environment variables. Drop
        never shows a value again: replace it to change it.
      </p>
      {secrets.length === 0 ? (
        <p className="empty">No secrets.</p>
      ) : (
        <ul className="apps" aria-label="Secrets">
          {secrets.map((secret) => (
            <li key={secret.name} className="row">
              <p>
                <code>{secret.name}</code>{" "}
                <span className="quiet">
                  version {secret.version} · {secret.length} bytes · updated{" "}
                  {formatDate(secret.updated)}
                </span>
                {secret.set_via === "mcp" && (
                  <span className="badge" title="The value passed through an assistant's conversation">
                    Set through MCP
                  </span>
                )}
              </p>
              <div className="row">
                <button
                  type="button"
                  className="secondary"
                  disabled={busy || app.archived}
                  onClick={() => {
                    setName(secret.name);
                    valueField.current?.focus();
                  }}
                >
                  Replace
                </button>
                <button
                  type="button"
                  className="secondary"
                  disabled={busy || app.archived}
                  onClick={() => void remove(secret)}
                >
                  Delete
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
      <form className="add" onSubmit={set} aria-label="Set a secret">
        <fieldset disabled={busy || app.archived}>
          <div className="row">
            <label>
              Secret name
              <input
                name="secret_name"
                value={name}
                maxLength={64}
                required
                autoCapitalize="characters"
                spellCheck={false}
                onChange={(event) => setName(event.target.value.trim())}
                placeholder="API_TOKEN"
              />
            </label>
            <label>
              Secret value
              <input
                ref={valueField}
                name="secret_value"
                type="password"
                value={value}
                maxLength={4096}
                required
                autoComplete="off"
                spellCheck={false}
                onChange={(event) => setValue(event.target.value)}
              />
            </label>
            <button className="primary">Save secret</button>
          </div>
        </fieldset>
      </form>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      <p role="status" className="quiet">
        {status}
      </p>
    </section>
  );
}

/** `#/apps/<id>`: one app with its Overview, Releases, Settings, Env & secrets and Usage tabs. */
export default function AppPage({ id, tab }: { id: string; tab: AppTab }) {
  if (tab === "deletion") return <DeletionStatus key={id} id={id} />;
  return <AppDetailPage id={id} tab={tab} />;
}

function AppDetailPage({ id, tab }: { id: string; tab: AppTab }) {
  const [app, setApp] = useState<App | null>(null);
  const [secrets, setSecrets] = useState<Secret[]>([]);
  const [updating, setUpdating] = useState("");
  const [missing, setMissing] = useState(false);
  const [error, setError] = useState("");
  const [version, setVersion] = useState(0);
  const mounted = useRef(true);
  const load = useCallback(async () => {
    try {
      const detail = await dropApi.getApp(id);
      if (!mounted.current) return;
      if (detail.deletion_pending) {
        window.location.hash = appHash(id, "deletion");
        return;
      }
      setApp(detail.app);
      setSecrets(detail.secrets);
      setError("");
    } catch (e) {
      if (!mounted.current) return;
      if (e instanceof OpError && e.status === 404) setMissing(true);
      else setError(message(e));
    }
  }, [id]);
  useEffect(() => {
    mounted.current = true;
    void load();
    let stop: (() => void) | undefined;
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
  const changed = (next: App) => {
    setApp(next);
    setVersion((v) => v + 1);
  };
  if (app?.beta_managed) return <>
    {!app.archived && (!app.current_release || updating === app.id)
      ? <BetaDeploy key={app.id} account={app.owner} initialFile={null} prepared={app} onComplete={() => { setUpdating(""); void load(); }} />
      : <BetaStatus id={app.id} name={app.name} release={app.current_release} hostname={app.hostname} />}
    {app.generic_beta && !app.archived && app.current_release && <button className="secondary"
      onClick={() => setUpdating(updating === app.id ? "" : app.id)}>{updating === app.id ? "Cancel update" : "Deploy a new release"}</button>}
    <DeleteApp app={app} />
  </>;
  if (missing)
    return (
      <section className="panel" aria-labelledby="not-found-title">
        <h2 id="not-found-title">Not found</h2>
        <p>
          This app does not exist or is not yours.{" "}
          <a href={viewHashes.apps}>Go to your apps</a>.
        </p>
      </section>
    );
  return (
    <section className="panel wide" aria-labelledby="app-title">
      <p>
        <a href={viewHashes.apps}>← Your apps</a>
      </p>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {!app ? (
        !error && <p className="quiet">Loading the app…</p>
      ) : (
        <>
          <h2 id="app-title">{app.name}</h2>
          {app.archived && (
            <p className="quiet">This app is archived. Restore it to change it.</p>
          )}
          <nav className="nav tabs" aria-label="App">
            {tabs.map((t) => (
              <a
                key={t.tab}
                href={appHash(app.id, t.tab)}
                aria-current={tab === t.tab ? "page" : undefined}
              >
                {t.label}
              </a>
            ))}
          </nav>
          {tab === "overview" && <Overview app={app} onChange={changed} />}
          {tab === "settings" && (
            // Remount after a change elsewhere so the form shows saved values.
            <Settings key={`${app.id}-${version}`} app={app} onChange={setApp} />
          )}
          {tab === "releases" && <Releases app={app} onChange={() => void load()} />}
          {tab === "usage" && <UsagePanel app={app} />}
          {tab === "env" && (
            <>
              <EnvVars app={app} secrets={secrets} onChange={changed} />
              <Secrets app={app} secrets={secrets} onChange={() => void load()} />
            </>
          )}
        </>
      )}
    </section>
  );
}
