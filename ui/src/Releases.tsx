import { useCallback, useEffect, useRef, useState, type DragEvent } from "react";
import { formatDate } from "./Apps";
import {
  dropApi,
  MAX_RELEASE_BYTES,
  releaseFileProblem,
  type App,
  type Release,
} from "./dropApi";
import { message } from "./errors";

function mib(bytes: number): string {
  return `${(bytes / 1048576).toFixed(1)} MiB`;
}

/**
 * An app's Releases tab: drag and drop (or choose) a linux/amd64 executable,
 * which goes to a one-time upload link with a progress bar, and the list of
 * releases with Deploy.
 */
export default function Releases({
  app,
  onChange,
}: {
  app: App;
  onChange: () => void;
}) {
  const [releases, setReleases] = useState<Release[] | null>(null);
  const [file, setFile] = useState<File | null>(null);
  const [problem, setProblem] = useState("");
  const [note, setNote] = useState("");
  const [activate, setActivate] = useState(true);
  const [progress, setProgress] = useState<{ loaded: number; total: number } | null>(null);
  const [dragging, setDragging] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [status, setStatus] = useState("");
  const input = useRef<HTMLInputElement>(null);
  const mounted = useRef(true);
  const load = useCallback(async () => {
    try {
      const list = await dropApi.listReleases(app.id);
      if (mounted.current) setReleases(list.releases);
    } catch (e) {
      if (mounted.current) setError(message(e));
    }
  }, [app.id]);
  useEffect(() => {
    mounted.current = true;
    void load();
    return () => {
      mounted.current = false;
    };
  }, [load, app.current_release]);

  const choose = async (chosen: File | undefined) => {
    setError("");
    setStatus("");
    setProgress(null);
    if (!chosen) return;
    setFile(chosen);
    try {
      setProblem(await releaseFileProblem(chosen));
    } catch (e) {
      setProblem(message(e));
    }
  };
  const drop = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    setDragging(false);
    if (!busy && !app.archived) void choose(event.dataTransfer.files[0]);
  };
  const upload = async () => {
    if (!file || problem) return;
    setBusy(true);
    setError("");
    setStatus("");
    setProgress({ loaded: 0, total: file.size });
    try {
      const link = await dropApi.createUploadLink(app.id, { note: note.trim(), activate });
      if (file.size > link.max_bytes) {
        setError(`The app file size limit is ${mib(link.max_bytes)}.`);
        return;
      }
      const result = await dropApi.uploadRelease(link.path, file, (loaded, total) => {
        if (mounted.current) setProgress({ loaded, total });
      });
      const short = result.release.sha256.slice(0, 12);
      setStatus(
        (result.created ? `Uploaded ${short}.` : `Already uploaded as ${short}.`) +
          (result.deployed ? ` Deployed: new deployment ${result.deployment_id}.` : ""),
      );
      setFile(null);
      setNote("");
      if (input.current) input.current.value = "";
      await load();
      onChange();
    } catch (e) {
      setError(message(e));
    } finally {
      if (mounted.current) setBusy(false);
    }
  };
  const deploy = async (release: Release) => {
    setBusy(true);
    setError("");
    setStatus("");
    try {
      const result = await dropApi.deployRelease(app.id, release.id);
      setStatus(
        result.deployment_bumped
          ? `Deployed ${release.sha256.slice(0, 12)}: new deployment ${result.deployment_id}.`
          : "That release is already current.",
      );
      await load();
      onChange();
    } catch (e) {
      setError(message(e));
    } finally {
      if (mounted.current) setBusy(false);
    }
  };

  const disabled = busy || app.archived;
  const percent = progress && progress.total > 0 ? Math.round((progress.loaded / progress.total) * 100) : 0;
  return (
    <section aria-labelledby="releases-title">
      <h3 id="releases-title">Releases</h3>
      <p className="quiet">
        For each release, upload a static Linux (amd64) ELF app file of at most {Math.floor(MAX_RELEASE_BYTES / 1048576)} MiB. To build one with Go, run{" "}
        <code>CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build</code>. It runs as{" "}
        <code>{app.executable_name}</code> on port {app.http_port}.
      </p>
      <div
        className={dragging ? "dropzone dragging" : "dropzone"}
        aria-disabled={disabled}
        onDragOver={(event) => {
          event.preventDefault();
          if (!disabled) setDragging(true);
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={drop}
      >
        <p>
          {file ? (
            <>
              <code>{file.name}</code> <span className="quiet">{mib(file.size)}</span>
            </>
          ) : (
            "Drop the app file here, or"
          )}
        </p>
        <label className="button secondary">
          {file ? "Choose another app file" : "Choose an app file"}
          <input
            ref={input}
            className="visually-hidden"
            type="file"
            aria-label="App file"
            disabled={disabled}
            onChange={(event) => void choose(event.target.files?.[0])}
          />
        </label>
      </div>
      {problem && (
        <p role="alert" className="error">
          {problem}
        </p>
      )}
      <fieldset disabled={disabled}>
        <div className="row">
          <label>
            Note (optional)
            <input
              name="release_note"
              value={note}
              maxLength={500}
              onChange={(event) => setNote(event.target.value)}
              placeholder="v1.2.0"
            />
          </label>
        </div>
        <label className="check">
          <input type="checkbox" checked={activate} onChange={(event) => setActivate(event.target.checked)} />
          Deploy it when the upload completes
        </label>
      </fieldset>
      <div className="row">
        <button
          type="button"
          className="primary"
          disabled={disabled || !file || problem !== ""}
          onClick={() => void upload()}
        >
          Upload app file
        </button>
      </div>
      {progress && (
        <p className="progress">
          <progress value={progress.loaded} max={progress.total || 1} aria-label="App file upload progress" />{" "}
          <span className="quiet">{percent}%</span>
        </p>
      )}
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      <p role="status" className="quiet">
        {status}
      </p>
      {releases === null ? (
        !error && <p className="quiet">Loading the releases…</p>
      ) : releases.length === 0 ? (
        <p className="empty">No releases yet.</p>
      ) : (
        <ul className="apps" aria-label="Releases">
          {releases.map((release) => (
            <li key={release.id} className="row">
              <p>
                <code title={release.sha256}>{release.sha256.slice(0, 12)}</code>{" "}
                <span className="quiet">
                  {mib(release.size_bytes)} · uploaded {formatDate(release.created)}
                </span>
                {release.note && <> · {release.note}</>}
                {release.current && <span className="badge">Current</span>}
              </p>
              {!release.current && (
                <button
                  type="button"
                  className="secondary"
                  disabled={disabled}
                  onClick={() => void deploy(release)}
                >
                  Deploy
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
