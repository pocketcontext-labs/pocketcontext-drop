import { AppBackupSetting } from "./Backups";
import PrivateBuildSource, {type Build} from "./PrivateBuildSource";
import CopyButton from "./CopyButton";
import "./releases.css";
import "./private-create.css";
import HelpTip from "./HelpTip";
import { useEffect, useState } from "react";
import { op, type App } from "./dropApi";
import { message } from "./errors";
import { appHash } from "./routes";
import RuntimeState from "./RuntimeState";
import AppAddress, { appURL } from "./AppAddress";
import OpenArrow from "./OpenArrow";
import { DeleteApp } from "./AppDeletion";

type Published = {
  prefix?: string;
  minimum_multiplier: number;
  rehearsed?: boolean;
  archived?: boolean;
  product: string;
  version: string;
  sha256: string;
  size_bytes: number;
  published: number;
};
type Submission = {
  app: string;
  release: string;
  submitted_by: string;
  product: string;
  version: string;
  sha256: string;
  size_bytes: number;
};
type Catalog = { enabled: boolean; releases: Published[] };
export function productName(id: string) {
  return id.split("-").map(word => word.charAt(0).toUpperCase() + word.slice(1)).join(" ");
}
function Size({
  value,
  recommended = 4,
  diskMiB,
  onChange,
}: {
  value: number;
  recommended?: number;
  diskMiB?: number;
  onChange: (n: number) => void;
}) {
  return (
    <div>
      <label className="size-label" htmlFor="workspace-size">Resources</label>{" "}
      <HelpTip topic="app size">CPU sets processing power, RAM holds work in progress, and disk stores files between runs. A larger size uses more credits while running. You can change it later in App settings.</HelpTip>
      <select id="workspace-size" aria-describedby="workspace-rate" value={value} onChange={(e) => onChange(Number(e.target.value))}>
        {Array.from({ length: 8 }, (_, i) => i + 1).map((n) => (
          <option value={n} key={n}>
            {n}× · {n / 4} vCPU · {n * 256} MiB RAM · {(diskMiB ?? n * 2048) / 1024} GiB disk{n === recommended ? " · recommended" : ""}
          </option>
        ))}
      </select>
      <small id="workspace-rate" className="quiet">{value} {value === 1 ? "credit" : "credits"} per awake hour</small>
    </div>
  );
}
function validCatalogPrefix(prefix: string) {
  return /^[a-z](?:[a-z0-9-]{0,14}[a-z0-9])?$/.test(prefix) && prefix !== "dev" && !prefix.startsWith("dev-");
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
  return <CreateWorkspace catalog={catalog} loadError={loadError} releaseSelection={releaseSelection} />;
}
export function CreateWorkspace({ catalog, loadError, releaseSelection = true, privateBuild = false }: {
  catalog?: Catalog; loadError?: string; releaseSelection?: boolean; privateBuild?: boolean;
}) {
  const [build, setBuild] = useState<Build>();
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
  const release = choices.find(r => r.sha256 === digest);
  const selected = privateBuild ? build : release;
  const products = Array.from(new Set(published.map(r => r.product))).map(id =>
    published.find(r => r.product === id && !r.archived) ?? published.find(r => r.product === id)
  ).filter((r): r is Published => !!r);
  function resetKey() {
    setKey(crypto.randomUUID());
  }
  return (
    <section className={privateBuild ? "wide official-page private-create" : "panel wide official-page"}>
      {privateBuild && <a className="private-back" href="#/apps">← Your apps</a>}
      <h2>
        {privateBuild ? "Create custom app" : product ? (`Create ${/^[aeiou]/i.test(product) ? "an" : "a"} ${productName(product)} app`) : "Catalog"}
      </h2>
      {catalog && !catalog.enabled ? (
        <p>The official catalog is awaiting activation.</p>
      ) : product || privateBuild ? (
        <form
          className={privateBuild ? "private-create-card app-create-form" : "stack app-create-form"}
          onSubmit={async (e) => {
            e.preventDefault();
            if (busy || !selected) return;
            setBusy(true);
            setError("");
            try {
              const settings = {name: name.trim(), multiplier: size};
              const r = privateBuild
                ? await op<{app: App}>("developer.build_create", { ...settings, sha256: selected.sha256 }, key)
                : await op<{app: App}>("workspace.create", {
                    ...settings, product,
                    ...(releaseSelection && {sha256: selected.sha256}),
                    ...(release?.archived && {allow_archived: true}),
                  }, key);
              location.hash = appHash(r.app.id);
            } catch (err) {
              setError(message(err));
            } finally {
              setBusy(false);
            }
          }}
        >
          <div className="stack">
          {privateBuild && <h3>Build &amp; Identity</h3>}
          {privateBuild && <PrivateBuildSource disabled={busy} onChange={result => {
            setBuild(result);
            if (result) setSize(result.minimum_multiplier || 1);
            if (result) setName(current => current.trim() ? current : "My " + productName(result.product));
            resetKey();
          }} />}
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
          {privateBuild ? (build && <p role="status">{productName(build.product)} · {build.version}</p>) : <label>
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
          </label>}
          {versions.some(r => r.archived) && <label className="release-opt-in">
            <input type="checkbox" checked={showArchived} onChange={e => {
              setShowArchived(e.target.checked);
              if (!e.target.checked && release?.archived) setDigest(versions.find(r => !r.archived)?.sha256 ?? "");
              resetKey();
            }} />
            Include archived releases
          </label>}
          </div>
          {privateBuild ? <fieldset className="private-size">
            <legend>Compute Size</legend>
            <div className="tier-grid">
              {Array.from({length: 8}, (_, index) => index + 1).map(n => (
                <label className="tier-option" key={n}>
                  <input type="radio" name="private-size" value={n} checked={size === n}
                    onChange={() => { setSize(n); resetKey(); }} />
                  <span><strong>{n}×</strong> {n === 4 && <small>Recommended</small>}
                    <span className="tier-spec">{n / 4} vCPU · {256 * n} MiB RAM · {2 * n} GiB disk</span>
                  </span>
                </label>
              ))}
            </div>
            <small className="quiet">{size} {size === 1 ? "credit" : "credits"} per awake hour</small>
          </fieldset> : <Size
            value={size}
            recommended={Math.max(4, release?.minimum_multiplier || 1)}
            onChange={(n) => {
              setSize(n);
              resetKey();
            }}
          />}
          <div className={privateBuild ? "private-create-footer" : "row"}>
            <button className="primary" disabled={busy || !selected}>
              Create app
            </button>
            {privateBuild ? <a href="#/apps">Cancel</a> : <button
              type="button"
              className="secondary"
              disabled={busy}
              onClick={() => setProduct("")}
            >
              Cancel
            </button>}
          </div>
        </form>
      ) : (
        <ul className="catalog-grid">
          {products.map((r) => (
            <li key={r.product}>
              <h3>{["crew", "atlas"].includes(r.product) && "PocketContext "}{productName(r.product)}</h3>
              {["crew", "atlas"].includes(r.product) && <p>
                {r.product === "crew"
                  ? "Notes, tasks and human review."
                  : "Your knowledge base and project files."}
              </p>}
              <small>{r.archived ? "Archived releases available" : "Version " + r.version}</small>
              <button
                className="primary"
                onClick={() => {
                  setProduct(r.product);
                  setShowArchived(false);
                  setDigest(r.archived ? "" : r.sha256);
                  setName("My " + productName(r.product));
                  setSize(Math.max(4, r.minimum_multiplier || 1));
                  resetKey();
                }}
              >
                Choose {productName(r.product)}
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
    [source, setSource] = useState("file"),
    [url, setURL] = useState(""),
    [file, setFile] = useState<File>(),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [status, setStatus] = useState("");
  const [prefixes, setPrefixes] = useState<Record<string, string>>({});
  const [submissions, setSubmissions] = useState<Submission[]>([]);
  const [submissionsError, setSubmissionsError] = useState("");
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    let active = true;
    op<{ submissions: Submission[] }>("catalog.submissions", {})
      .then(result => {
        if (active) {
          setSubmissions(result.submissions);
          setSubmissionsError("");
        }
      })
      .catch(e => { if (active) setSubmissionsError(message(e)); });
    return () => { active = false; };
  }, [revision]);

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
          void act(async () => {
            if (source === "url") {
              await op("catalog.import_url", { product, version, url: url.trim() });
            } else {
              if (!file) throw new Error("Choose a release file.");
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
            }
            return "Manifest checked. Attach a successful data rehearsal before publishing.";
          });
        }}
      >
        <h3>Stage a release</h3>
        <label>
          Source
          <select value={source} onChange={e => {
            setSource(e.target.value);
            setFile(undefined);
            setError("");
            setStatus("");
          }}>
            <option value="file">File</option>
            <option value="url">Download URL</option>
          </select>
        </label>
        <label>
          App
          <input value={product} required maxLength={32} onChange={e => setProduct(e.target.value)} />
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
        {source === "url" ? <label>
          Release URL
          <input type="url" placeholder="https://" required value={url} onChange={e => setURL(e.target.value)} />
        </label> : <label>
          Linux executable or archive
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
        </label>}
        <button className="primary" disabled={busy}>
          {source === "url" ? "Download and check" : "Upload and check"}
        </button>
      </form>
      {(error || loadError) && <p role="alert">{error || loadError}</p>}
      {status && <p role="status">{status}</p>}
      {submissionsError && <p role="alert">Could not load submitted releases. {submissionsError}{" "}
        <button className="link" onClick={() => setRevision(n => n + 1)}>Retry</button>
      </p>}
      {submissions?.length > 0 && <section aria-labelledby="submitted-title">
        <h3 id="submitted-title">Submitted for review</h3>
        <ul className="release-list">
          {submissions.map(candidate => (
            <li className="release-card" key={candidate.release}>
              <div className="release-info">
                <strong>{productName(candidate.product)} {candidate.version}</strong>
                <p className="release-meta">Submitted by {candidate.submitted_by} · {(candidate.size_bytes / 1048576).toFixed(1)} MiB</p>
                <code className="candidate-digest">sha256: {candidate.sha256}</code>
              </div>
              <button className="secondary" disabled={busy} onClick={() => void act(async () => {
                await op("catalog.accept", { app: candidate.app, release: candidate.release, sha256: candidate.sha256 });
                setRevision(n => n + 1);
                return "Candidate accepted. Attach a successful data rehearsal before publishing.";
              })}>Accept for rehearsal</button>
            </li>
          ))}
        </ul>
      </section>}
      <ul className="release-list">
        {catalog?.releases.map((r) => (
          <li key={r.sha256} className="release-card">
            <div className="release-info">
              <h3>
                {productName(r.product)} {r.version}
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
                  label={`Copy SHA-256 for ${productName(r.product)} ${r.version}`}
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
                        `Archive ${productName(r.product)} ${r.version}? Existing apps keep their installed version.`,
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
              {!r.published && !r.prefix && <label>Catalog prefix
                <input value={prefixes[r.product] ?? r.product} maxLength={16} onChange={e => setPrefixes({...prefixes, [r.product]: e.target.value})} />
              </label>}
              {!r.published && (
                <p>
                  <button
                    className="primary"
                    disabled={busy || !r.rehearsed || (!r.prefix && !validCatalogPrefix(prefixes[r.product] ?? r.product))}
                    onClick={() =>
                      void act(async () => {
                        await op("catalog.publish", { sha256: r.sha256, ...(!r.prefix && {prefix: prefixes[r.product] ?? r.product}) });
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
            !catalog.releases.some(r => !r.archived && r.published)
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
  const latest = catalog?.releases.find(r => r.product === app.official_product);
  return <WorkspaceDetail app={app} reload={reload} latest={latest} loadError={loadError} />;
}

export function WorkspaceDetail({ app, reload, latest, loadError, children }: {
  app: App;
  reload: () => void;
  latest?: Published;
  loadError?: string;
  children?: React.ReactNode;
}) {
  const [size, setSize] = useState(app.memory_mib / 256),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [review, setReview] = useState(false);
  const resizing = app.disk_resize?.outcome === "pending";
  useEffect(() => setSize(app.memory_mib / 256), [app.id, app.memory_mib]);
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
      <section className="panel app-status-card">
        <a className="status-back" href="#/apps">← Apps</a>
        <header className="status-header">
          <div className="status-identity"><h1>{app.name}</h1><RuntimeState key={app.id} id={app.id} />
            {app.developer_private && <span className="custom-app-label">Custom</span>}</div>
          {appURL(app.hostname) && <a className="button primary" href={appURL(app.hostname)} target="_blank" rel="noreferrer">Open app <OpenArrow /></a>}
        </header>
        <AppAddress hostname={app.hostname} />
        {app.desired_state === "on-request" && !app.archived && <p className="quiet">HTTP traffic wakes this app automatically. Compute charges pause while it sleeps. Stored data can still incur S3 charges.</p>}
      </section>
      <section className="panel official-page">
        <h2>App settings</h2>
        <p>
          {app.current_release || app.official_product ? <>
            Installed · {productName(app.official_product || app.executable_name)}{" "}
            {app.installed_version || (app.official_product ? "· awaiting first healthy deployment" : "")}
          </> : "Upload a release to run this app."}
        </p>
        {latest &&
          latest.sha256 !== app.beta_sha256 && !resizing &&
          (!review ? (
            <button className="secondary" onClick={() => setReview(true)}>
              Review upgrade to {latest.version}
            </button>
          ) : (
            <div>
              <p>
                Install {productName(latest.product)} {latest.version} at{" "}
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
        {resizing && <p role="status">Resizing the disk for {app.disk_resize?.multiplier}× resources. Your current configuration stays in effect until the node confirms the change.</p>}
        {app.disk_resize?.outcome === "refused" && <p role="alert">The node could not resize the disk for {app.disk_resize.multiplier}× resources. Your original disk and resources are intact.</p>}
        {(app.official_product || app.private_tiered) && <>
        <Size
          value={size}
          recommended={app.minimum_multiplier || 4}
          diskMiB={app.disk_resize_supported ? undefined : app.disk_mib}
          onChange={setSize}
        />
        <button
          className="secondary"
          disabled={busy || app.archived || resizing || (size === app.memory_mib / 256 && (!app.disk_resize_supported || size * 2048 === app.disk_mib))}
          onClick={() =>
            void mutate(() =>
              op("workspace.resize", { app: app.id, multiplier: size }),
            )
          }
        >
          Save resources
        </button>
        </>}
        {app.archived && app.developer_private && <button className="secondary" disabled={busy} onClick={() => void mutate(() => op("app.restore", {app: app.id}))}>Restore app</button>}
        {(error || loadError) && <p role="alert">{error || loadError}</p>}
        {app.official_product === "crew" && !app.archived && <AppBackupSetting app={app.id} />}
        {!resizing && <div className="workspace-delete"><DeleteApp app={app} /></div>}
      </section>
      {children}
    </div>
  );
}
