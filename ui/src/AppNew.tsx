import { useState, type FormEvent } from "react";
import { NAME_LIMIT, validName, validSlug } from "./api";
import {
  dropApi,
  type DesiredState,
  type HealthKind,
} from "./dropApi";
import { message } from "./errors";
import { appHash, viewHashes } from "./routes";

/** Number fields of an app with their bounds (internal/appspec). */
export const limits = {
  idle_timeout_ms: { min: 60000, max: 86400000, label: "Idle timeout (ms)" },
  vcpu: { min: 1, max: 2, label: "vCPU" },
  memory_mib: { min: 128, max: 4096, label: "Memory (MiB)" },
  disk_mib: { min: 256, max: 16384, label: "Disk (MiB)" },
  hook_port: { min: 0, max: 65535, label: "Hooks port" },
  http_port: { min: 1, max: 65535, label: "HTTP port" },
} as const;
export type NumberField = keyof typeof limits;

export function NumberInput({
  field,
  value,
  onChange,
  disabled,
}: {
  field: NumberField;
  value: number;
  onChange: (value: number) => void;
  disabled?: boolean;
}) {
  const { min, max, label } = limits[field];
  return (
    <label>
      {label}
      <input
        type="number"
        name={field}
        min={min}
        max={max}
        step={1}
        required
        disabled={disabled}
        value={Number.isNaN(value) ? "" : value}
        onChange={(event) => onChange(event.target.valueAsNumber)}
      />
      {field === "hook_port" && <small>0 disables lifecycle hooks.</small>}
      {field === "http_port" && value > 0 && value < 1024 && (
        <small>
          Ports below 1024 require a guest image that permits binding them.
        </small>
      )}
    </label>
  );
}

/** `#/apps/new`: create an app with its name, slug and settings. */
export default function AppNew() {
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [state, setState] = useState<DesiredState>("on-request");
  const [executable, setExecutable] = useState("server");
  const [health, setHealth] = useState<HealthKind>("tcp");
  const [healthPath, setHealthPath] = useState("/");
  const [numbers, setNumbers] = useState<Record<NumberField, number>>({
    http_port: 8080,
    hook_port: 0,
    vcpu: 1,
    memory_mib: 512,
    disk_mib: 1024,
    idle_timeout_ms: 300000,
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const input = {
        name: validName(name),
        slug: validSlug(slug),
        kind: "ordinary" as const,
        desired_state: state,
        executable_name: executable.trim(),
        health_kind: health,
        ...(health === "http" ? { health_path: healthPath.trim() } : {}),
        ...numbers,
      };
      const app = await dropApi.createApp(input);
      location.hash = appHash(app.id);
    } catch (e) {
      setError(message(e));
      setBusy(false);
    }
  };
  const number = (field: NumberField) => (
    <NumberInput
      field={field}
      value={numbers[field]}
      onChange={(value) => setNumbers((n) => ({ ...n, [field]: value }))}
    />
  );
  return (
    <section className="new-app-page wide" aria-labelledby="new-app-title">
      <header>
        <a className="new-app-back" href={viewHashes.apps}>
          ← Your apps
        </a>
        <h2 id="new-app-title">New app</h2>
      </header>
      <form className="new-app-card" onSubmit={submit}>
        <fieldset>
          <legend>Identity</legend>
          <div className="new-app-grid identity">
            <label>
              App name
              <input
                name="name"
                value={name}
                maxLength={NAME_LIMIT}
                required
                onChange={(event) => setName(event.target.value)}
                placeholder="Hello app"
              />
            </label>
          </div>
          <label>
            Slug
            <input
              name="slug"
              value={slug}
              maxLength={32}
              required
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              aria-describedby="new-app-slug-help"
              onChange={(event) => setSlug(event.target.value)}
              placeholder="hello-app"
            />
          </label>
          <p id="new-app-slug-help" className="quiet">
            Choose a unique slug using 3–32 lowercase letters, digits or
            hyphens. The slug is permanent.
          </p>
        </fieldset>
        <fieldset>
          <legend>Runtime &amp; networking</legend>
          <div className="new-app-grid runtime">
            <label>
              State
              <select
                value={state}
                onChange={(event) =>
                  setState(event.target.value as DesiredState)
                }
              >
                <option value="on-request">
                  On request (sleeps when idle)
                </option>
                <option value="running">Running</option>
                <option value="stopped">Stopped</option>
              </select>
            </label>
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
            {number("http_port")}
            {number("hook_port")}
          </div>
          <div className="new-app-grid identity">
            <label>
              Health check
              <select
                value={health}
                onChange={(event) =>
                  setHealth(event.target.value as HealthKind)
                }
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
        </fieldset>
        <fieldset>
          <legend>Resources</legend>
          <div className="new-app-grid resources">
            {number("vcpu")}
            {number("memory_mib")}
            {number("disk_mib")}
            {state === "on-request" && number("idle_timeout_ms")}
          </div>
        </fieldset>
        <footer className="new-app-footer">
          <p className="quiet">
            The disk cannot change once the app runs on a node. Add environment
            variables and secrets on the app's page.
          </p>
          {error && (
            <p role="alert" className="error">
              {error}
            </p>
          )}
          <button
            className="primary"
            disabled={busy || !name.trim() || !slug.trim()}
          >
            Create app
          </button>
        </footer>
      </form>
    </section>
  );
}
