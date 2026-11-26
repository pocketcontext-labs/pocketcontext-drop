import { useEffect, useRef, useState, type DragEvent } from "react";
import Brand from "./Brand";
import type { PublishedLegal } from "./api";
import CopyButton from "./CopyButton";
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

function explain(e: unknown, fallback: string) {
  if (e instanceof OpError) {
    const messages: Record<string, string> = {
      unsupported_binary:
        "This release is not supported. Choose an approved PocketContext binary.",
      probe_pending: "Drop is checking this release. Try again in a moment.",
      beta_no_capacity:
        "There is no space for this deployment right now. Please retry later.",
      quota_exceeded: "Your account has reached its app limit.",
      invalid_input:
        "This selection is unavailable. Check the release and size again.",
    };
    return messages[e.code] ?? fallback;
  }
  return fallback;
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
          ? "Choose the binary inside the folder."
          : "Choose exactly one binary at a time.",
      );
      return;
    }
    setError("");
    let problem = "";
    try {
      if (/\.(zip|tar|gz|bz2|xz|tgz|7z)$/i.test(chosen.name))
        problem = "Extract the archive and choose the binary inside.";
      else if (chosen.size > MAX_RELEASE_BYTES)
        problem = "Choose a binary of 90 MiB or less.";
      else if (await releaseFileProblem(chosen))
        problem = "Choose a supported PocketContext Linux binary.";
    } catch {
      problem = "The file could not be read. Please choose it again.";
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
        <strong>Drop your PocketContext binary here</strong>
        <p>One executable, up to 90 MiB</p>
        <button
          className="primary"
          onClick={() => input.current?.click()}
          disabled={disabled}
        >
          Choose a file
        </button>
        <input
          ref={input}
          type="file"
          hidden
          aria-label="PocketContext binary"
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
        <button className="secondary" onClick={onLogin}>
          Sign in
        </button>
      </header>
      <section className="beta-intro">
        <h1>Deploy your app</h1>
        <p>
          Choose your PocketContext binary to get started. An invitation is
          required to deploy.
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
  no_release: "Waiting for a release",
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
    !["running", "sleeping"].includes(status.phase) ||
    !/^[a-z0-9-]+(?:\.[a-z0-9-]+)+$/i.test(status.hostname)
  )
    return "";
  return "https://" + status.hostname;
}

export function BetaStatus({
  id,
  name,
  release,
}: {
  id: string;
  name: string;
  release: string;
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
              "The deployment is taking longer than expected. Check again for an update.",
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
      {url && (
        <>
          <p className="beta-url">{url}</p>
          <div className="beta-actions">
            <a
              className="button primary"
              href={url}
              target="_blank"
              rel="noreferrer"
            >
              Open app
            </a>
            <CopyButton text={url} />
          </div>
        </>
      )}
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
  account,
  prepared,
  onComplete,
}: {
  initialFile: File | null;
  account: string;
  prepared?: App;
  onComplete?: () => void;
}) {
  const [file, setFile] = useState(initialFile);
  const [product, setProduct] = useState<BetaRelease>();
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
    if (file || prepared?.beta_sha256) {
      const inspect = async () => {
        try {
          let sha = prepared?.beta_sha256 ?? "";
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
                  "Choose the same PocketContext release used to start this deployment.",
                );
              return;
            }
            sha = selected;
          }
          if (!active) return;
          const info = await dropApi.inspectBeta(sha);
          if (!active) return;
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
            prepared ? prepared.memory_mib / 256 : (info.multipliers[0] ?? 0),
          );
        } catch (e) {
          if (active)
            setError(
              explain(e, "Could not check this file. Please try again."),
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
    prepared?.id,
    prepared?.beta_sha256,
    prepared?.memory_mib,
  ]);

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
    const storageKey = ["drop.beta.prepare", account, digest, size].join(".");
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
      if (!draft.app)
        draft.app = (await dropApi.prepareBeta(digest, size, draft.key)).app.id;
      if (!mounted.current) return;
      if (!draft.release && file) {
        setAction("Uploading binary…");
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
      <h1>{prepared ? "Resume deployment" : "Deploy your app"}</h1>
      {prepared && !file && !product && !error && (
        <p role="status">Checking your upload…</p>
      )}
      {prepared && product && !stagedRelease && !file && (
        <p>Select your original binary to finish the upload.</p>
      )}
      {!file && !stagedRelease && (!prepared || product) && (
        <FilePicker onSelect={setFile} />
      )}
      {file && (
        <>
          <p className="beta-filename">{file.name}</p>
          {!locked && (
            <button className="link" onClick={() => setFile(null)}>
              Choose another file
            </button>
          )}
          {!product && !error && <p role="status">Checking release…</p>}
        </>
      )}
      {product && (
        <>
          <h2>
            {product.name} {product.version}
          </h2>
          {product.multipliers.length ? (
            <>
              <label>
                App size
                <select
                  value={size}
                  disabled={locked || !!prepared}
                  onChange={(event) => setSize(Number(event.target.value))}
                >
                  {product.multipliers.map((n) => (
                    <option key={n} value={n}>
                      {n}×
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
                  busy ||
                  (!file && !stagedRelease) ||
                  !product.multipliers.includes(size)
                }
                onClick={() => void deploy()}
              >
                {error && locked ? "Retry deployment" : "Deploy app"}
              </button>
            </>
          ) : (
            <p>No sizes are available for this release on your account.</p>
          )}
        </>
      )}
      {busy && (
        <p role="status">
          {action}{" "}
          {upload !== null && (
            <>
              <progress aria-label="Upload progress" value={upload} max={100} />{" "}
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
