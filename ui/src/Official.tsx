import CopyButton from "./CopyButton";
import "./releases.css";
import HelpTip from "./HelpTip";
import { useEffect, useState } from "react";
import { op, type App } from "./dropApi";
import { message } from "./errors";
import { appHash } from "./routes";
import { BetaStatus } from "./BetaDeploy";
import { DeleteApp } from "./AppDeletion";

type Published = {
  minimum_multiplier: number;
  rehearsed?: boolean;
  archived?: boolean;
  product: string;
  version: string;
  sha256: string;
  size_bytes: number;
  published: number;
};
type Catalog = { enabled: boolean; releases: Published[] };
const names: Record<string, string> = { crew: "Crew", atlas: "Atlas" };
function Size({
  value,
  minimum = 1,
  separateRate = false,
  onChange,
}: {
  value: number;
  minimum?: number;
  separateRate?: boolean;
  onChange: (n: number) => void;
}) {
  return (
    <div>
      <label className="size-label" htmlFor="workspace-size">Resources</label>{" "}
      <HelpTip topic="app size">The size sets how much memory and processing power your app gets. A larger size uses more credits while running. You can change it later in App settings.</HelpTip>
      <select id="workspace-size" aria-describedby={separateRate ? "workspace-rate" : undefined} value={value} onChange={(e) => onChange(Number(e.target.value))}>
        {Array.from({ length: 9 - minimum }, (_, i) => i + minimum).map((n) => (
          <option value={n} key={n}>
            {n}×{n === 4 ? " · recommended" : ""}
            {!separateRate && ` · ${n} ${n === 1 ? "credit" : "credits"} per awake hour`}
          </option>
        ))}
      </select>
      {separateRate && <small id="workspace-rate" className="quiet">{value} {value === 1 ? "credit" : "credits"} per awake hour</small>}
    </div>
  );
}
function useCatalog(allVersions = false) {
  const [catalog, setCatalog] = useState<Catalog>();
  const [version, refresh] = useState(0),
    [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    op<Catalog>("catalog.list", allVersions ? { all_versions: true, include_archived: true } : {})
      .then((v) => {
        if (active) {
          setCatalog(v);
          setError("");
        }
      })
      .catch((e) => {
        if (active) setError(message(e));
      });
    return () => {
      active = false;
    };
  }, [version, allVersions]);
  return { catalog, error, refresh: () => refresh((v) => v + 1) };
}
export function OfficialCatalog({ releaseSelection = true }: { releaseSelection?: boolean }) {
  const { catalog, error: loadError } = useCatalog(releaseSelection);
  const [product, setProduct] = useState(""),
    [name, setName] = useState(""),
    [size, setSize] = useState(4),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const [key, setKey] = useState(() => crypto.randomUUID());
  const [showArchived, setShowArchived] = useState(false);
  const [digest, setDigest] = useState("");
  const published = (catalog?.releases ?? []).filter(r => r.published > 0)
    .sort((a, b) => b.published - a.published);
  const versions = published.filter(r => r.product === product);
  const choices = versions.filter(r => showArchived || !r.archived);
  const selected = choices.find(r => r.sha256 === digest);
  const minimum = selected?.minimum_multiplier || 1;
  const products = Object.keys(names).map(id =>
    published.find(r => r.product === id && !r.archived) ?? published.find(r => r.product === id)
  ).filter((r): r is Published => !!r);
  function resetKey() {
    setKey(crypto.randomUUID());
  }
  return (
    <section className="panel wide official-page">
      <h2>
        {product ? ("Create " + (product === "atlas" ? "an " : "a ") + names[product] + " app") : "Catalog"}
      </h2>
      {product ? (
        <form
          className="stack app-create-form"
          onSubmit={async (e) => {
            e.preventDefault();
            if (busy || !selected) return;
            setBusy(true);
            setError("");
            try {
              const r = await op<{ app: App }>(
                "workspace.create",
                { product, name, multiplier: Math.max(size, minimum), ...(releaseSelection && { sha256: selected.sha256 }), ...(selected.archived && { allow_archived: true }) },
                key,
              );
              location.hash = appHash(r.app.id);
            } catch (err) {
              setError(message(err));
            } finally {
              setBusy(false);
            }
          }}
        >
          <label>
            App name
            <input
              required
              maxLength={80}
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                resetKey();
              }}
            />
          </label>
          <label>
            Release
            <select required value={selected?.sha256 ?? ""} onChange={e => {
              setDigest(e.target.value);
              resetKey();
            }}>
              {!selected && <option value="" disabled>Choose a release</option>}
              {choices.map(r => <option key={r.sha256} value={r.sha256}>
                {r.version}{r.archived ? " (archived)" : ""}
              </option>)}
            </select>
          </label>
          {versions.some(r => r.archived) && <label className="release-opt-in">
            <input type="checkbox" checked={showArchived} onChange={e => {
              setShowArchived(e.target.checked);
              if (!e.target.checked && selected?.archived) setDigest(versions.find(r => !r.archived)?.sha256 ?? "");
              resetKey();
            }} />
            Include archived releases
          </label>}
          <Size separateRate
            value={Math.max(size, minimum)}
            minimum={minimum}
            onChange={(n) => {
              setSize(n);
              resetKey();
            }}
          />
          <div className="row">
            <button className="primary" disabled={busy || !selected}>
              Create app
            </button>
            <button
              type="button"
              className="secondary"
              disabled={busy}
              onClick={() => setProduct("")}
            >
              Cancel
            </button>
          </div>
        </form>
      ) : (
        <ul className="catalog-grid">
          {products.map((r) => (
            <li key={r.product}>
              <h3>PocketContext {names[r.product]}</h3>
              <p>
                {r.product === "crew"
                  ? "Notes, tasks and human review."
                  : "Your knowledge base and project files."}
              </p>
              <small>{r.archived ? "Archived releases available" : "Version " + r.version}</small>
              <button
                className="primary"
                onClick={() => {
                  setProduct(r.product);
                  setShowArchived(false);
                  setDigest(r.archived ? "" : r.sha256);
                  setName("My " + names[r.product]);
                  setSize(Math.max(4, r.minimum_multiplier || 1));
                  resetKey();
                }}
              >
                Choose {names[r.product]}
              </button>
            </li>
          ))}
        </ul>
      )}
      {(error || loadError) && <p role="alert">{error || loadError}</p>}
    </section>
  );
}
export function OfficialReleases({
  onActivated,
}: {
  onActivated?: () => void;
}) {
  const { catalog, error: loadError, refresh } = useCatalog();
  const [product, setProduct] = useState("crew"),
    [version, setVersion] = useState(""),
    [file, setFile] = useState<File>(),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [status, setStatus] = useState("");
  async function act(fn: () => Promise<string>) {
    if (busy) return;
    setBusy(true);
    setError("");
    setStatus("");
    try {
      setStatus(await fn());
      refresh();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="release-page">
      <h2>Releases</h2>
      <form
        className="release-upload"
        onSubmit={(e) => {
          e.preventDefault();
          if (!file) return;
          void act(async () => {
            const { path } = await op<{ path: string }>("catalog.upload", {
              product,
              version,
            });
            const res = await fetch(path, {
              method: "PUT",
              body: file,
              credentials: "omit",
              redirect: "error",
            });
            if (!res.ok) {
              const body = await res.json().catch(() => ({}));
              throw new Error(body.message || "The release check failed.");
            }
            return "Manifest checked. Attach a successful data rehearsal before publishing.";
          });
        }}
      >
        <h3>Upload a release</h3>
        <label>
          App
          <select value={product} onChange={(e) => setProduct(e.target.value)}>
            <option value="crew">Crew</option>
            <option value="atlas">Atlas</option>
          </select>
        </label>
        <label>
          Version
          <input
            required
            placeholder="e.g. 0.45.5-drop.1"
            value={version}
            onChange={(e) => setVersion(e.target.value)}
            maxLength={80}
          />
        </label>
        <label>
          Linux executable
          <span className="release-file">
            <span className="release-file-button" aria-hidden="true">
              Choose file
            </span>
            <span
              className="release-file-name"
              aria-hidden="true"
              title={file?.name}
            >
              {file?.name || "No file selected"}
            </span>
            <input
              aria-label="Linux executable"
              type="file"
              required
              onChange={(e) => setFile(e.target.files?.[0])}
            />
          </span>
        </label>
        <button className="primary" disabled={busy}>
          Upload and check
        </button>
      </form>
      {(error || loadError) && <p role="alert">{error || loadError}</p>}
      {status && <p role="status">{status}</p>}
      <ul className="release-list">
        {catalog?.releases.map((r) => (
          <li key={r.sha256} className="release-card">
            <div className="release-info">
              <h3>
                {names[r.product]} {r.version}
              </h3>
              <p className="release-meta">
                <span
                  className={
                    r.published && !r.archived
                      ? "release-state published"
                      : "release-state"
                  }
                >
                  {r.archived
                    ? "Archived"
                    : r.published
                      ? "Published"
                      : r.rehearsed
                        ? "Rehearsal recorded"
                        : "Manifest checked"}
                </span>
                {(r.size_bytes / 1048576).toFixed(1)} MiB
              </p>
              <div className="release-digest">
                <code title={r.sha256}>
                  sha256: {r.sha256.slice(0, 8)}…{r.sha256.slice(-7)}
                </code>
                <CopyButton
                  text={r.sha256}
                  icon
                  label={`Copy SHA-256 for ${names[r.product]} ${r.version}`}
                />
              </div>
            </div>
            <div className="release-actions">
              {!r.published && (
                <label>
                  Data rehearsal receipt (JSON)
                  <input
                    type="file"
                    accept="application/json,.json"
                    disabled={busy}
                    onChange={(e) => {
                      const receipt = e.target.files?.[0];
                      e.target.value = "";
                      if (!receipt) return;
                      void act(async () => {
                        if (receipt.size > 16000)
                          throw new Error(
                            "Choose a rehearsal receipt under 16 KB.",
                          );
                        const report = JSON.parse(await receipt.text());
                        if (report.sha256 !== r.sha256)
                          throw new Error(
                            "This receipt belongs to another executable.",
                          );
                        await op("catalog.rehearse", report);
                        return "Rehearsal evidence recorded.";
                      });
                    }}
                  />
                </label>
              )}
              {!!r.published && !r.archived && (
                <button
                  className="secondary release-archive"
                  disabled={busy}
                  onClick={() => {
                    if (
                      window.confirm(
                        `Archive ${names[r.product]} ${r.version}? Existing apps keep their installed version.`,
                      )
                    )
                      void act(async () => {
                        await op("catalog.archive", { sha256: r.sha256 });
                        return "Release archived.";
                      });
                  }}
                >
                  Archive release
                </button>
              )}
              {!r.published && (
                <p>
                  <button
                    className="primary"
                    disabled={busy || !r.rehearsed}
                    onClick={() =>
                      void act(async () => {
                        await op("catalog.publish", { sha256: r.sha256 });
                        return "Release published. Existing apps keep their installed versions.";
                      })
                    }
                  >
                    Publish release
                  </button>
                </p>
              )}
            </div>
          </li>
        ))}
      </ul>
      {catalog && !catalog.enabled && (
        <button
          className="secondary"
          disabled={
            busy ||
            !["crew", "atlas"].every((product) =>
              catalog.releases.some(
                (r) => r.product === product && !r.archived && r.published,
              ),
            )
          }
          onClick={() =>
            void act(async () => {
              await op("catalog.activate");
              onActivated?.();
              return "The official app catalog is active.";
            })
          }
        >
          Enable official app catalog
        </button>
      )}
    </section>
  );
}
export function OfficialWorkspace({
  app,
  reload,
}: {
  app: App;
  reload: () => void;
}) {
  const { catalog, error: loadError } = useCatalog();
  const [size, setSize] = useState(app.memory_mib / 256),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [review, setReview] = useState(false);
  const latest = catalog?.releases.find(
    (r) => r.product === app.official_product,
  );
  async function mutate(fn: () => Promise<unknown>) {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      await fn();
      setReview(false);
      reload();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="workspace-detail">
      <BetaStatus
        key={app.deployment_id}
        id={app.id}
        hostname={app.hostname}
        name={app.name}
        release={app.current_release}
        officialDigest={app.beta_sha256}
      />
      <section className="panel official-page">
        <h2>App settings</h2>
        <p>
          Installed · {names[app.official_product!]}{" "}
          {app.installed_version || "· awaiting first healthy deployment"}
        </p>
        {latest &&
          latest.sha256 !== app.beta_sha256 &&
          (!review ? (
            <button className="secondary" onClick={() => setReview(true)}>
              Review upgrade to {latest.version}
            </button>
          ) : (
            <div>
              <p>
                Install {names[latest.product]} {latest.version} at{" "}
                {app.hostname}? Your app will restart on its existing
                volume.
              </p>
              <button
                className="primary"
                disabled={busy}
                onClick={() =>
                  void mutate(() =>
                    op("workspace.upgrade", {
                      app: app.id,
                      sha256: latest.sha256,
                    }),
                  )
                }
              >
                Install upgrade
              </button>{" "}
              <button className="secondary" onClick={() => setReview(false)}>
                Defer
              </button>
            </div>
          ))}
        {(app.minimum_multiplier ?? 1) > 1 && (
          <p className="quiet">
            This installed release requires at least {app.minimum_multiplier}×.
            Upgrade to a release with a lower minimum to select smaller sizes.
          </p>
        )}
        <Size
          value={size}
          minimum={app.minimum_multiplier}
          onChange={setSize}
        />
        <button
          className="secondary"
          disabled={busy || size === app.memory_mib / 256}
          onClick={() =>
            void mutate(() =>
              op("workspace.resize", { app: app.id, multiplier: size }),
            )
          }
        >
          Save resources
        </button>
        {(error || loadError) && <p role="alert">{error || loadError}</p>}
        <div className="workspace-delete"><DeleteApp app={app} /></div>
      </section>
    </div>
  );
}
