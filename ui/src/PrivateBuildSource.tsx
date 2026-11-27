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
  const [url, setURL] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function inspect(file?: File) {
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
    <fieldset className="stack" disabled={disabled || busy}>
      <legend>Private build</legend>
      <label>
        Source
        <select
          value={source}
          onChange={(e) => {
            setSource(e.target.value);
            onChange();
            setError("");
          }}
        >
          <option value="file">Executable file</option>
          <option value="url">Download URL</option>
        </select>
      </label>
      {source === "file" ? (
        <label>
          Linux executable
          <input
            type="file"
            onChange={(e) => {
              const file = e.target.files?.[0];
              if (file) void inspect(file);
              else onChange();
            }}
          />
        </label>
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
        The executable must support --drop-manifest. Private apps receive a dev-
        hostname.
      </small>
      {busy && <span role="status">Checking build…</span>}
      {error && <span role="alert">{error}</span>}
    </fieldset>
  );
}
