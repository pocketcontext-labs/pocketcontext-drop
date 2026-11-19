import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { api, NAME_LIMIT, type App } from "./api";
import { message } from "./errors";

const dateFormat = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
});
export function formatDate(value: string): string {
  // PocketBase stores "YYYY-MM-DD HH:MM:SS.sssZ" in UTC.
  const date = new Date(value.replace(" ", "T"));
  return Number.isNaN(date.getTime()) ? value : dateFormat.format(date);
}

/** The project's apps, newest first, and a form to add one. */
export default function Apps() {
  const [apps, setApps] = useState<App[] | null>(null);
  const [error, setError] = useState("");
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");
  const mounted = useRef(true);
  const reads = useRef(0);
  const load = useCallback(async () => {
    const read = ++reads.current;
    try {
      const rows = await api.listApps();
      if (mounted.current && read === reads.current) {
        setApps(rows);
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
    // Changes from the CLI or another tab appear without a reload.
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
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    setStatus("");
    try {
      await api.addApp(name, slug);
      if (!mounted.current) return;
      setName("");
      setSlug("");
      setStatus("App added.");
      await load();
    } catch (e) {
      if (mounted.current) setError(message(e));
    } finally {
      if (mounted.current) setBusy(false);
    }
  };
  const remaining = NAME_LIMIT - name.trim().length;
  return (
    <section className="panel" aria-labelledby="apps-title">
      <h2 id="apps-title">Your apps</h2>
      <form className="add" onSubmit={submit}>
        <label htmlFor="new-app-name">App name</label>
        <input
          id="new-app-name"
          name="name"
          value={name}
          maxLength={NAME_LIMIT}
          required
          aria-describedby="app-name-remaining"
          onChange={(event) => setName(event.target.value)}
          placeholder="Hello app"
        />
        <p id="app-name-remaining" className="quiet">
          {remaining} characters left
        </p>
        <label htmlFor="new-app-slug">Slug</label>
        <div className="row">
          <input
            id="new-app-slug"
            name="slug"
            value={slug}
            maxLength={32}
            required
            autoCapitalize="none"
            autoCorrect="off"
            spellCheck={false}
            aria-describedby="app-slug-help"
            onChange={(event) => setSlug(event.target.value)}
            placeholder="hello-app"
          />
          <button
            className="primary"
            disabled={busy || !name.trim() || !slug.trim()}
          >
            Add
          </button>
        </div>
        <p id="app-slug-help" className="quiet">
          3-32 lowercase letters, digits and hyphens; unique
        </p>
      </form>
      <p role="status" className="quiet">
        {status}
      </p>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {apps === null ? (
        !error && <p className="quiet">Loading apps…</p>
      ) : apps.length === 0 ? (
        <p className="empty">No apps yet. Add the first one above.</p>
      ) : (
        <ol className="apps" aria-label="Apps, newest first">
          {apps.map((app) => (
            <li key={app.id}>
              <p>
                <span>{app.name}</span> <code className="slug">{app.slug}</code>
              </p>
              <time dateTime={app.created.replace(" ", "T")}>
                {formatDate(app.created)}
              </time>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
