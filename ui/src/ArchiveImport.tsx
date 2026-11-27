import { useState } from "react";
import type { DeployRecipe } from "./deployRecipes";
import { dropApi, MAX_RELEASE_BYTES, releaseFileProblem } from "./dropApi";
import {
  archiveName,
  bundleRelease,
  digest,
  inspectArchive,
  standaloneELF,
  type Archive,
  type LaunchOptions,
} from "./releaseArchive";

type Props = {
  input: File | null;
  recipe?: DeployRecipe;
  onReady: (file: File, port: number) => void;
  onCancel: () => void;
};

function stringArray(raw: string, label: string): string[] {
  try {
    const parsed: unknown = JSON.parse(raw);
    if (
      Array.isArray(parsed) &&
      parsed.every((x) => typeof x === "string" && !x.includes("\0"))
    )
      return parsed;
  } catch {
    /* Give the owner the field name below. */
  }
  throw Error(label + " must be a JSON array of strings.");
}
export default function ArchiveImport({
  input,
  recipe,
  onReady,
  onCancel,
}: Props) {
  const [url, setURL] = useState(recipe?.releaseUrl ?? "");
  const [expected, setExpected] = useState(recipe?.sha256 ?? "");
  const [archive, setArchive] = useState<Archive>();
  const [selected, setSelected] = useState(recipe?.executable ?? "");
  const [args, setArgs] = useState(JSON.stringify(recipe?.args ?? []));
  const [env, setEnv] = useState(JSON.stringify(recipe?.env ?? {}));
  const [assets, setAssets] = useState(JSON.stringify(recipe?.assets ?? []));
  const [seed, setSeed] = useState("");
  const [port, setPort] = useState(recipe?.port ?? 8080);
  const [error, setError] = useState("");
  const [step, setStep] = useState("");
  const [busy, setBusy] = useState(false);
  const [completed, setCompleted] = useState<string[]>([]);

  async function inspect() {
    setBusy(true);
    setError("");
    setArchive(undefined);
    setCompleted([]);
    try {
      if (expected && !/^[a-f0-9]{64}$/i.test(expected))
        throw Error("Enter a 64-character hexadecimal SHA-256.");
      let file = input;
      if (!file) {
        const source = new URL(url);
        if (source.protocol !== "https:" || source.username || source.password)
          throw Error("Enter a public HTTPS release URL.");
        setStep("Downloading release…");
        const imported = await dropApi.importURL(url, expected || undefined);
        // Decode in aligned chunks to avoid an array of one object per byte
        // for large upstream archives.
        const chunks: BlobPart[] = [];
        for (let i = 0; i < imported.content_base64.length; i += 65536) {
          const raw = atob(imported.content_base64.slice(i, i + 65536));
          const bytes = new Uint8Array(raw.length);
          for (let j = 0; j < raw.length; j++) bytes[j] = raw.charCodeAt(j);
          chunks.push(bytes);
        }
        file = new File(chunks, source.pathname.split("/").pop() || "release");
        setCompleted(["Downloaded release"]);
      }
      if (file.size > MAX_RELEASE_BYTES)
        throw Error("Releases are limited to 90 MiB.");
      const magic = new Uint8Array(await file.slice(0, 2).arrayBuffer());
      let result: Archive;
      if (archiveName(file.name) || magic[0] === 0x50 || magic[0] === 0x1f) {
        result = await inspectArchive(file, expected, (status) => {
          if (status.startsWith("Extracting"))
            setCompleted((rows) => [
              ...rows,
              expected
                ? "Verified archive checksum"
                : "Computed archive checksum",
            ]);
          setStep(status);
        });
      } else {
        const problem = await releaseFileProblem(file);
        if (problem) throw Error(problem);
        if (!(await standaloneELF(file)))
          throw Error(
            "Choose a statically linked Linux (amd64) executable with a valid entry point.",
          );
        const hash = await digest(file);
        if (expected && expected.toLowerCase() !== hash)
          throw Error("Release checksum mismatch.");
        result = {
          digest: hash,
          files: new Map([[file.name, file]]),
          candidates: [file.name],
        };
      }
      setArchive(result);
      setSelected(
        recipe?.executable && result.candidates.includes(recipe.executable)
          ? recipe.executable
          : result.candidates.length === 1
            ? result.candidates[0]
            : "",
      );
      if (
        recipe?.executable &&
        !result.candidates.includes(recipe.executable)
      ) {
        setSelected("");
        throw Error(
          "The pinned executable is missing or incompatible. Review the archive and choose a compatible file.",
        );
      }
      setStep("Release verified and inspected.");
    } catch (e) {
      setStep("");
      setError(e instanceof Error ? e.message : "Release import failed.");
    } finally {
      setBusy(false);
    }
  }

  async function prepare() {
    if (!archive) return;
    setBusy(true);
    setError("");
    setStep("Packaging the selected executable and assets…");
    try {
      if (!Number.isInteger(port) || port < 1 || port > 65535)
        throw Error("Choose an HTTP port from 1 to 65535.");
      const variables: unknown = JSON.parse(env);
      if (
        !variables ||
        typeof variables !== "object" ||
        Array.isArray(variables) ||
        !Object.entries(variables).every(
          ([k, v]) =>
            /^[A-Z_][A-Z0-9_]*$/.test(k) &&
            typeof v === "string" &&
            !v.includes("\0"),
        )
      )
        throw Error(
          "Enter non-secret environment settings as a JSON object of string values.",
        );
      const options: LaunchOptions = {
        executable: selected,
        args: stringArray(args, "Arguments"),
        env: variables as Record<string, string>,
        assets: stringArray(assets, "Asset paths"),
        seed,
      };
      const raw = archive.files.get(selected);
      // Plain executable URL imports retain their original digest and manifest.
      if (
        raw instanceof File &&
        !options.args.length &&
        !Object.keys(options.env).length &&
        !options.assets.length &&
        !seed
      )
        onReady(raw, port);
      else onReady(await bundleRelease(archive, options), port);
    } catch (e) {
      setStep("");
      setError(
        e instanceof Error ? e.message : "Could not package the release.",
      );
    } finally {
      setBusy(false);
    }
  }
  const invalidate = () => {
    setArchive(undefined);
    setCompleted([]);
    setStep("");
  };
  return (
    <section className="beta-flow" aria-label="Release import">
      <h2>{recipe?.name ?? "Import a release"}</h2>
      {recipe?.minimal && (
        <p>
          Review the {recipe.name} release for public HTTPS on port {port}.
        </p>
      )}
      <details open={!recipe?.minimal}>
        <summary>Source and launch settings</summary>
        {input ? (
          <p>{input.name}</p>
        ) : (
          <label>
            HTTPS release URL
            <input
              type="url"
              value={url}
              disabled={busy}
              onChange={(e) => {
                setURL(e.target.value);
                invalidate();
              }}
            />
          </label>
        )}
        <label>
          Expected SHA-256 (optional)
          <input
            value={expected}
            disabled={busy}
            onChange={(e) => {
              setExpected(e.target.value);
              invalidate();
            }}
          />
        </label>
        <label>
          Launch arguments (JSON array)
          <textarea
            value={args}
            disabled={busy}
            onChange={(e) => setArgs(e.target.value)}
          />
        </label>
        <label>
          HTTP port
          <input
            type="number"
            min={1}
            max={65535}
            value={port}
            disabled={busy}
            onChange={(e) => setPort(Number(e.target.value))}
          />
        </label>
        <label>
          Non-secret environment settings (JSON object)
          <textarea
            value={env}
            disabled={busy}
            onChange={(e) => setEnv(e.target.value)}
          />
        </label>
        <label>
          Packaged asset paths (JSON array)
          <textarea
            value={assets}
            disabled={busy}
            onChange={(e) => setAssets(e.target.value)}
          />
        </label>
        <label>
          Initial data directory (optional)
          <input
            value={seed}
            disabled={busy}
            onChange={(e) => setSeed(e.target.value)}
          />
        </label>
        <p>
          Public HTTPS access. Initial data seeds an empty volume under
          /app/data using the directory’s name.
        </p>
      </details>
      {archive && (
        <>
          <p>
            Archive SHA-256 <code>{archive.digest}</code>
          </p>
          <label>
            Executable
            <select
              value={selected}
              disabled={busy}
              onChange={(e) => setSelected(e.target.value)}
            >
              <option value="">Choose an executable</option>
              {archive.candidates.map((path) => (
                <option key={path}>{path}</option>
              ))}
            </select>
          </label>
          {selected && (
            <p>
              Command{" "}
              <code>
                {selected} {args}
              </code>
            </p>
          )}
        </>
      )}
      {recipe?.executable && !archive && (
        <p>
          Executable <code>{recipe.executable}</code>
        </p>
      )}
      {completed.length > 0 && (
        <ul aria-label="Completed import steps">
          {completed.map((label) => (
            <li key={label}>{label}</li>
          ))}
        </ul>
      )}
      {step && <p role="status">{step}</p>}
      {error && <p role="alert">{error}</p>}
      {archive ? (
        <button
          className="primary"
          disabled={busy || !selected}
          onClick={() => void prepare()}
        >
          Use this release
        </button>
      ) : (
        <button
          className="primary"
          disabled={busy || (!input && !url)}
          onClick={() => void inspect()}
        >
          Inspect release
        </button>
      )}
      <button className="secondary" disabled={busy} onClick={onCancel}>
        Choose another release
      </button>
    </section>
  );
}
