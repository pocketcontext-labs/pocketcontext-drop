import { useEffect, useRef, useState, type DragEvent } from "react";
import Brand from "./Brand";
import ArchiveImport from "./ArchiveImport";
import type {DeployRecipe} from "./deployRecipes";
import {archiveName} from "./releaseArchive";
import Credits from "./Credits";
import CopyButton from "./CopyButton";
import type { PublishedLegal } from "./api";
import AppAddress, { appURL } from "./AppAddress";
import { BetaLegalLinks } from "./BetaLegal";
import {
  dropApi,
  MAX_RELEASE_BYTES,
  OpError,
  releaseFileProblem,
  type App,
  type AppStatus,
  type BetaRelease,
} from "./dropApi";
import { appHash, viewHashes } from "./routes";

const serviceNames: Record<string,string> = {
  persistent_volume: "Local disk",
  s3_replica: "Platform S3 (credits required)",
  sign_in_email: "Sign-in email",
  verified_owner_email: "Verified owner email",
};

function explain(e: unknown, fallback: string) {
  if (e instanceof OpError) {
    const messages: Record<string, string> = {
      unsupported_binary:
        "This app file is not supported. Choose a different Linux (amd64) app file.",
      probe_pending: "Drop is checking this app file. Try again in a moment.",
      beta_no_capacity:
        "There is no space for this deployment right now. Please retry later.",
      storage_credits_required: "This app requires platform S3. Buy credits to deploy it.",
      quota_exceeded: "This deployment would exceed your retained-disk allowance.",
      invalid_input:
        "This selection is unavailable. Check the app file and size again.",
    };
    return messages[e.code] ?? fallback;
  }
  return e instanceof Error ? e.message : fallback;
}

function FilePicker({
  onSelect,
  disabled,
}: {
  onSelect: (file: File) => void;
  disabled?: boolean;
}) {
  const [error, setError] = useState("");
  const [dragging, setDragging] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const generation = useRef(0);
  const select = async (files: FileList | null, folder = false) => {
    const attempt = ++generation.current;
    if (disabled) return;
    const chosen = files?.[0];
    if (folder || !chosen || files.length !== 1) {
      setError(
        folder
          ? "Choose the app file inside the folder."
          : "Choose one app file at a time.",
      );
      return;
    }
    setError("");
    let problem = "";
    try {
      if (chosen.size > MAX_RELEASE_BYTES)
        problem = "Choose an app file of 90 MiB or less.";
      else if (archiveName(chosen.name)) {
        const signature = new Uint8Array(await chosen.slice(0,2).arrayBuffer());
        if (signature[0] !== 0x50 && signature[0] !== 0x1f) problem = "Choose a valid ZIP or tar.gz archive.";
      } else if (await releaseFileProblem(chosen))
        problem = "Choose a Linux (amd64) app file for Linux (amd64).";
    } catch {
      problem = "The app file could not be read. Please choose it again.";
    }
    if (attempt !== generation.current) return;
    if (problem) setError(problem);
    else onSelect(chosen);
  };
  const drop = (event: DragEvent) => {
    event.preventDefault();
    setDragging(false);
    const folder = Array.from(event.dataTransfer.items ?? []).some(
      (item) => item.webkitGetAsEntry?.()?.isDirectory,
    );
    void select(event.dataTransfer.files, folder);
  };
  return (
    <>
      <div
        className={"beta-picker" + (dragging ? " dragging" : "")}
        onDragOver={(event) => {
          event.preventDefault();
          if (!disabled) setDragging(true);
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={drop}
      >
        <strong>Drop your app file here</strong>
        <p>Linux (amd64) executables, ZIP or tar.gz archives, up to 90 MiB</p>
        <button
          className="primary"
          onClick={() => input.current?.click()}
          disabled={disabled}
        >
          Choose an app file
        </button>
        <input
          ref={input}
          type="file"
          hidden
          aria-label="App file"
          disabled={disabled}
          onChange={(event) => {
            void select(event.target.files);
            event.target.value = "";
          }}
        />
      </div>
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
    </>
  );
}

export function BetaLanding({
  documents,
  onSelect,
  onLogin,
}: {
  onSelect: (file: File) => void;
  onLogin: () => void;
  documents?: PublishedLegal;
}) {
  return (
    <main className="beta-landing">
      <header>
        <Brand />
        <div className="waitlist-actions">
        <a className="button secondary" href="#/waitlist">Join waitlist</a>
        <button className="secondary" onClick={onLogin}>
          Sign in
        </button>
        </div>
      </header>
      <section className="beta-intro">
        <h1>Deploy your PocketContext app</h1>
        <p>
          You need an invitation to deploy.
        </p>
        <FilePicker onSelect={onSelect} />
        <BetaLegalLinks documents={documents} />
      </section>
    </main>
  );
}

const failurePhases = [
  "staging_failed",
  "secret_failed",
  "unhealthy",
  "failed",
  "stopped",
];
const phases: Record<string, string> = {
  pending: "Waiting to start",
  staging: "Preparing your app",
  deploying: "Deploying your app",
  starting: "Starting your app",
  unknown: "Waiting for a status update",
  unplaced: "Waiting for space",
  no_release: "Waiting for an app file",
  running: "Checking the app address",
  sleeping: "Checking the app address",
};

// After a retry the previous deployment's terminal report can briefly be
// the newest observation. Keep polling until the node reports the new ID.
function deploymentPhase(status?: AppStatus) {
  if (status?.reported_deployment_id && status.reported_deployment_id !== status.deployment_id &&
      failurePhases.includes(status.phase)) return "deploying";
  return status?.phase;
}

function liveUrl(status?: AppStatus) {
  if (
    status?.url_ready !== true ||
    !status.deployment_id ||
    status.deployment_id !== status.reported_deployment_id ||
    !["running", "sleeping"].includes(status.phase)
  )
    return "";
  return appURL(status.hostname);
}

export function BetaStatus({
  id,
  name,
  release,
  hostname,
}: {
  id: string;
  name: string;
  release: string;
  hostname: string;
}) {
  const [status, setStatus] = useState<AppStatus>();
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    const deadline = Date.now() + 90000;
    async function poll() {
      try {
        const result = await dropApi.appStatus(id, true);
        if (!active) return;
        setStatus(result);
        setError("");
        if (
          !liveUrl(result) &&
          !failurePhases.includes(deploymentPhase(result) ?? "") &&
          result.phase !== "archived"
        ) {
          if (Date.now() < deadline) timer = setTimeout(poll, 4000);
          else
            setError(
              result.phase === "running" || result.phase === "sleeping"
                ? "The public HTTP check has not succeeded. Check the configured port and the private startup output, then check again."
                : "The deployment is taking longer than expected. Check again for an update.",
            );
        }
      } catch {
        if (active)
          setError(
            "Could not check the deployment. Check your connection and try again.",
          );
      }
    }
    void poll();
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [id, refresh]);
  const phase = deploymentPhase(status);
  const failed = phase && failurePhases.includes(phase);
  const url = liveUrl(status);
  const processStarted = status?.reported_deployment_id === status?.deployment_id &&
    (phase === "running" || phase === "sleeping");
  const setup = status?.first_run;
  const expires = setup ? Date.parse(setup.expires_at) : 0;
  useEffect(() => {
    if (!expires || expires <= Date.now()) return;
    const timer = window.setTimeout(() => setRefresh(x => x + 1), expires - Date.now() + 1);
    return () => window.clearTimeout(timer);
  }, [expires]);
  const activeSetup = url && expires > Date.now() ? setup : undefined;
  const retry = async () => {
    setBusy(true);
    try {
      await dropApi.deployBeta(id, release);
      setStatus(undefined);
      setError("");
      setRefresh((x) => x + 1);
    } catch (e) {
      setError(
        explain(e, "Could not restart the deployment. Please try again."),
      );
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="panel beta-flow">
      <a href={viewHashes.apps}>Your apps</a>
      <h1>{name}</h1>
      <p role="status">
        {url
          ? "Your app is live."
          : failed
            ? "Your app could not start."
            : phase === "archived"
              ? "This app is archived."
              : (phases[phase ?? ""] ?? "Checking deployment…")}
      </p>
      {release && phase !== "archived" && (
        <ol aria-label="Deployment progress">
          <li>Release verification: complete</li>
          <li>Startup: {processStarted ? "complete" : failed ? "failed" : "waiting"}</li>
          <li>Public HTTP check: {url ? "complete" : processStarted ? "awaiting a successful response" : "waiting for startup"}</li>
        </ol>
      )}
      <AppAddress hostname={status?.hostname || hostname} />
      {url && (
        <div className="beta-actions">
          <a className="button primary" href={url} target="_blank" rel="noreferrer">
            Open app
          </a>
          {!activeSetup && (
            <button className="secondary" onClick={() => setRefresh(x => x + 1)}>Refresh deployment status</button>
          )}
          {activeSetup?.kind === "pocketbase_installer" && (
            <a className="button secondary" href={url + "/_/#/pbinstall/" + encodeURIComponent(activeSetup.value)} target="_blank" rel="noreferrer">
              Open PocketBase installer
            </a>
          )}
          {activeSetup?.kind === "zeroclaw_pairing" && (
            <div>
              <p>Open the dashboard and enter this private pairing code.</p>
              <code>{activeSetup.value}</code>{" "}
              <CopyButton text={activeSetup.value} label="Copy pairing code" />
            </div>
          )}
        </div>
      )}
      {status?.log_tail && <details open={!!failed || !!error}><summary>Private startup output</summary><pre>{status.log_tail}</pre></details>}
      {failed && status && <details>
        <summary>Deployment diagnostics</summary>
        <p>{status.message}</p>
        <dl><dt>Restarts</dt><dd>{status.restart_count}</dd>
          {status.last_exit_code != null && <><dt>Last exit code</dt><dd>{status.last_exit_code}</dd></>}
          <dt>Last observed</dt><dd>{status.observed_at || "Waiting for a report"}</dd>
        </dl>
      </details>}
      {failed && release && (
        <button
          className="primary"
          disabled={busy}
          onClick={() => void retry()}
        >
          Retry deployment
        </button>
      )}
      {error && (
        <>
          <p role="alert" className="error">
            {error}
          </p>
          <button
            className="secondary"
            onClick={() => setRefresh((x) => x + 1)}
          >
            Check again
          </button>
        </>
      )}
    </section>
  );
}

export default function BetaDeploy({
  initialFile,
  recipe,
  account,
  prepared,
  onComplete,
}: {
  initialFile: File | null;
  recipe?: DeployRecipe;
  account: string;
  prepared?: App;
  onComplete?: () => void;
}) {
  const updating = !!prepared?.current_release;
  const [archiveFile,setArchiveFile] = useState(initialFile && archiveName(initialFile.name) ? initialFile : null);
  const [importing,setImporting] = useState(!!archiveFile || !!recipe);
  const [file, setFile] = useState(importing ? null : initialFile);
  const [hostname, setHostname] = useState(prepared?.hostname ?? "");
  const [product, setProduct] = useState<BetaRelease>();
  const [port, setPort] = useState(8080);
  const [digest, setDigest] = useState("");
  const [size, setSize] = useState(0);
  const [error, setError] = useState("");
  const [inspectCount, setInspectCount] = useState(0);
  const [upload, setUpload] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [action, setAction] = useState("");
  const attempt = useRef({ app: "", release: "", key: "" });
  const [stagedRelease, setStagedRelease] = useState("");
  const [locked, setLocked] = useState(false);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  useEffect(() => {
    let active = true;
    setProduct(undefined);
    setError("");
    setSize(0);
    setDigest("");
    setLocked(false);
    setStagedRelease("");
    attempt.current = { app: prepared?.id ?? "", release: "", key: "" };
    if (file || (!updating && prepared?.beta_sha256)) {
      const inspect = async () => {
        try {
          let sha = (!updating && prepared?.beta_sha256) || "";
          if (file) {
            const hash = await crypto.subtle.digest(
              "SHA-256",
              await file.arrayBuffer(),
            );
            const selected = Array.from(new Uint8Array(hash), (value) =>
              value.toString(16).padStart(2, "0"),
            ).join("");
            if (sha && sha !== selected) {
              if (active)
                setError(
                  "Choose the same app file you used to start this deployment.",
                );
              return;
            }
            sha = selected;
          }
          if (!active) return;
          let info: BetaRelease;
          try {
            info = await dropApi.inspectBeta(sha);
          } catch (err) {
            if (!file || !(err instanceof OpError) || err.code !== "probe_pending") throw err;
            const {path} = await dropApi.probeUpload(sha);
            if (!active) return;
            const inspected = await fetch(path, {method: "PUT", credentials:"omit", redirect:"error", body:file});
            if (!inspected.ok) throw new Error("Drop could not inspect the file.");
            info = await dropApi.inspectBeta(sha);
          }
          if (!active) return;
          if (updating && !info.generic) throw Error("Choose a generic release for this app.");
          if (prepared) {
            const { releases } = await dropApi.listReleases(prepared.id);
            if (!active) return;
            const staged =
              releases.find((release) => release.sha256 === sha)?.id ?? "";
            setStagedRelease(staged);
            attempt.current.release = staged;
          }
          setDigest(sha);
          setProduct(info);
          setSize(
            prepared
              ? prepared.memory_mib / 256
              : (info.suggested_multiplier ?? info.multipliers[0] ?? 0),
          );
        } catch (e) {
          if (active)
            setError(
              explain(e, "Could not check this app file. Please try again."),
            );
        }
      };
      void inspect();
    }
    return () => {
      active = false;
    };
  }, [
    file,
    inspectCount,
    updating,
    prepared?.id,
    prepared?.beta_sha256,
    prepared?.memory_mib,
  ]);

  function chooseFile(next: File | null) {
    setProduct(undefined);
    setSize(0);
    setPort(8080);
    setDigest("");
    const isArchive = !!next && archiveName(next.name);
    setArchiveFile(isArchive ? next : null);
    setImporting(isArchive);
    setFile(isArchive ? null : next);
  }
  async function refreshStorageEntitlement() {
    try {
      const info = await dropApi.inspectBeta(digest);
      if (mounted.current) setProduct(info);
    } catch (e) {
      if (mounted.current) setError(explain(e, "Could not refresh the storage balance. Try again."));
    }
  }
  async function deploy() {
    if (
      (!file && !stagedRelease) ||
      !product?.multipliers.includes(size) ||
      busy
    )
      return;
    setBusy(true);
    setError("");
    setLocked(true);
    const draft = attempt.current;
    const keyParts = ["drop.beta.prepare", account, digest, size, product.generic ? port : "manifest"];
    if (product.generic && recipe?.name) keyParts.push(recipe.name);
    const storageKey = keyParts.join(".");
    try {
      // Reuse the preparation key if the connection or tab closes after creating the app.
      if (!draft.key && !draft.app) {
        try {
          draft.key = sessionStorage.getItem(storageKey) ?? "";
        } catch {
          /* Optional storage. */
        }
        draft.key ||= crypto.randomUUID();
        try {
          sessionStorage.setItem(storageKey, draft.key);
        } catch {
          /* Optional storage. */
        }
      }
      setAction("Preparing deployment…");
      if (!draft.app) {
        const { app } = await dropApi.prepareBeta(digest, size, draft.key, product.generic ? port : undefined, product.generic ? recipe?.name : undefined);
        draft.app = app.id;
        // A refresh reopens the server-backed app and any already uploaded release.
        history.replaceState(history.state, "", appHash(app.id));
        if (mounted.current) setHostname(app.hostname);
      }
      if (!mounted.current) return;
      if (!draft.release && file) {
        setAction("Uploading app file…");
        setUpload(0);
        const link = await dropApi.createUploadLink(draft.app, {
          activate: false,
        });
        if (!mounted.current) return;
        const result = await dropApi.uploadRelease(
          link.path,
          file,
          (loaded, total) => {
            if (mounted.current && total)
              setUpload(Math.round((loaded / total) * 100));
          },
        );
        draft.release = result.release.id;
      }
      if (!mounted.current) return;
      setUpload(null);
      setAction("Starting deployment…");
      await dropApi.deployBeta(draft.app, draft.release);
      try {
        sessionStorage.removeItem(storageKey);
      } catch {
        /* Optional storage. */
      }
      if (mounted.current) {
        if (onComplete) onComplete();
        else if (location.hash === appHash(draft.app)) window.dispatchEvent(new HashChangeEvent("hashchange"));
        else location.hash = appHash(draft.app);
      }
    } catch (e) {
      if (mounted.current) {
        setError(
          explain(
            e,
            "Deployment could not finish. Check your connection and retry.",
          ),
        );
        if (e instanceof OpError && e.code === "storage_credits_required") {
          setProduct(info => info && ({...info, needs_storage_credits:true}));
        }
        if (e instanceof OpError && e.status === 400 && !draft.app)
          setLocked(false);
      }
    } finally {
      if (mounted.current) {
        setBusy(false);
        setAction("");
        setUpload(null);
      }
    }
  }
  return (
    <section className="panel beta-flow">
      <h1>{updating ? "Deploy a new release" : prepared ? "Resume deployment" : "Deploy your PocketContext app"}</h1>
      <AppAddress hostname={hostname} />
      {prepared && !updating && !file && !product && !error && (
        <p role="status">Checking your app file…</p>
      )}
      {prepared && product && !stagedRelease && !file && (
        <p>Select your original app file to finish the upload.</p>
      )}
      {importing ? <ArchiveImport input={archiveFile} recipe={recipe} fixedPort={prepared?.http_port} onCancel={()=>chooseFile(null)}
        onReady={(ready, httpPort)=>{setImporting(false);setPort(prepared?.http_port ?? httpPort);setFile(ready);}} /> :
        !file && !stagedRelease && (updating || !prepared || product) && <>
          <FilePicker onSelect={chooseFile} />
          <button className="secondary" onClick={()=>setImporting(true)}>Import from HTTPS URL</button>
        </>}
      {file && (
        <>
          <p className="beta-filename">{file.name}</p>
          {!locked && (
            <button className="link" onClick={() => chooseFile(null)}>
              Choose another app file
            </button>
          )}
          {!product && !error && <p role="status">Checking app file…</p>}
        </>
      )}
      {product && (
        <>
          <h2>
            {product.name} {product.version}
          </h2>
          {product.multipliers.length ? (
            <>
              {!product.generic && product.http_port && <p>HTTP port {product.http_port}.</p>}
              {!!product.requires?.length && <p>Required services: {product.requires.map(x => serviceNames[x]).join(", ")}.</p>}
              {!!product.optional?.length && <p>Optional services: {product.optional.map(x => serviceNames[x]).join(", ")}.</p>}
              {product.needs_storage_credits && <>
                <p>This app requires platform S3. Buy credits to continue with this file.</p>
                <Credits admin={false} onPurchase={() => void refreshStorageEntitlement()} />
              </>}
              <p>Local disk is a cache. Your app is responsible for backups.</p>
              {product.generic && <>
                <p>This app has no valid Drop manifest. There is no guarantee it will start. It receives no platform S3 or email.</p>
                <label>HTTP port<input type="number" min="1" max="65535" disabled={locked || !!prepared}
                    value={prepared?.http_port ?? port} onChange={e => setPort(Number(e.target.value))} /></label>
              </>}
              <label>
                App size
                <select
                  value={size}
                  disabled={locked || !!prepared}
                  onChange={(event) => setSize(Number(event.target.value))}
                >
                  {product.multipliers.map((n) => (
                    <option key={n} value={n}>
                      {n}×{n === product.suggested_multiplier ? " (suggested)" : ""}
                    </option>
                  ))}
                </select>
              </label>
              <dl className="beta-resources" aria-live="polite">
                <div>
                  <dt>Memory</dt>
                  <dd>{size * 256} MB</dd>
                </div>
                <div>
                  <dt>CPU</dt>
                  <dd>{size / 4} vCPU</dd>
                </div>
                <div>
                  <dt>Storage</dt>
                  <dd>{size * 2} GB</dd>
                </div>
              </dl>
              <button
                className="primary"
                disabled={
                  busy || product.needs_storage_credits || (product.generic && (!Number.isInteger(port) || port < 1 || port > 65535)) ||
                  (!file && !stagedRelease) ||
                  !product.multipliers.includes(size)
                }
                onClick={() => void deploy()}
              >
                {error && locked ? "Retry deployment" : "Deploy app"}
              </button>
            </>
          ) : (
            <p>No sizes are available for this PocketContext app on your account.</p>
          )}
        </>
      )}
      {busy && (
        <p role="status">
          {action}{" "}
          {upload !== null && (
            <>
              <progress aria-label="App file upload progress" value={upload} max={100} />{" "}
              {upload}%
            </>
          )}
        </p>
      )}
      {error && (
        <>
          <p role="alert" className="error">
            {error}
          </p>
          {!product && (
            <button
              className="secondary"
              onClick={() => setInspectCount((x) => x + 1)}
            >
              Check again
            </button>
          )}
        </>
      )}
    </section>
  );
}
