import { useState } from "react";
import { op } from "./dropApi";
import { message } from "./errors";

export type Build = {
  product: string;
  version: string;
  sha256: string;
  minimum_multiplier: number;
};

export default function PrivateBuildSource({
  onChange,
  disabled,
}: {
  onChange: (build?: Build) => void;
  disabled: boolean;
}) {
  const [source, setSource] = useState("file");
  const [file, setFile] = useState<File>();
  const [url, setURL] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function inspect(file?: File) {
    setFile(file);
    onChange();
    setError("");
    setBusy(true);
    try {
      let build: Build;
      if (file) {
        const { path } = await op<{ path: string }>(
          "developer.build_upload",
          {},
        );
        const response = await fetch(path, {
          method: "PUT",
          body: file,
          credentials: "omit",
          redirect: "error",
        });
        const result = await response.json();
        if (!response.ok)
          throw new Error(result.message || "Could not check the executable.");
        build = result;
      } else {
        build = await op<Build>("developer.build_import", { url: url.trim() });
      }
      if (!build.sha256 || !build.product || !build.version)
        throw new Error("The build did not return a valid Drop manifest.");
      onChange(build);
    } catch (err) {
      setError(message(err));
    } finally {
      setBusy(false);
    }
  }
  return (
    <fieldset aria-label="Build source" className="stack" disabled={disabled || busy}>
      <label>
        Source
        <select
          value={source}
          onChange={(e) => {
            setSource(e.target.value);
            setFile(undefined);
            onChange();
            setError("");
          }}
        >
          <option value="file">Executable file</option>
          <option value="url">Download URL</option>
        </select>
      </label>
      {source === "file" ? (
        <div>
          <label htmlFor="private-file">Linux executable</label>
          <div className="private-file-row"
            onDragOver={e => e.preventDefault()}
            onDrop={e => {
              e.preventDefault();
              const dropped = e.dataTransfer.files[0];
              if (dropped && !disabled && !busy) void inspect(dropped);
            }}>
            <svg aria-hidden="true" width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6">
              <path d="M12 16V3m-5 5 5-5 5 5M4 16v4h16v-4" />
            </svg>
            <span>{file ? <><code>{file.name}</code><small>{file.size < 1024 ? file.size + " B" : file.size < 1048576 ? (file.size / 1024).toFixed(1) + " KiB" : (file.size / 1048576).toFixed(1) + " MiB"}</small></> : "Drop your executable here"}</span>
            <span className="file-action">{file ? "Change" : "Choose file"}</span>
            <input id="private-file" type="file" onChange={e => {
              const chosen = e.target.files?.[0];
              if (chosen) void inspect(chosen);
              e.target.value = "";
            }} />
          </div>
        </div>
      ) : (
        <>
          <label>
            Executable URL
            <input
              type="url"
              placeholder="https://"
              value={url}
              onChange={(e) => {
                setURL(e.target.value);
                onChange();
                setError("");
              }}
            />
          </label>
          <div>
            <button
              className="secondary"
              type="button"
              disabled={!url.trim()}
              onClick={() => void inspect()}
            >
              Check build
            </button>
          </div>
        </>
      )}
      <small className="quiet">
        The executable must support <code>--drop-manifest</code>. Private apps receive a <code>dev-</code>
        hostname.
      </small>
      {busy && <span role="status">Checking build…</span>}
      {error && <span role="alert">{error}</span>}
    </fieldset>
  );
}
