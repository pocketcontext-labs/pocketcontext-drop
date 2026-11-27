/** Public upstream release metadata for the reader's generic deployment form. */
export interface DeployRecipe {
  id: string;
  name: string;
  purpose: string;
  category: string;
  projectUrl: string;
  version: string;
  releaseUrl: string;
  sha256: string;
  executable: string;
  args: string[];
  port: number;
  env: Record<string, string>;
  assets: string[];
  minimal?: boolean;
}

export const deployRecipes: DeployRecipe[] = [
  {
    id: "pocketbase",
    name: "PocketBase",
    purpose: "Run a database with authentication, file storage and an admin dashboard.",
    category: "Backends",
    projectUrl: "https://github.com/pocketbase/pocketbase",
    version: "v0.40.6",
    releaseUrl: "https://github.com/pocketbase/pocketbase/releases/download/v0.40.6/pocketbase_0.40.6_linux_amd64.zip",
    sha256: "0e1f609158ed945f809f8a02ae012aa0427129c66ebb63c1aee76f2c0309373f",
    executable: "pocketbase",
    args: ["serve", "--http=0.0.0.0:8090", "--dir=./data/pb_data", "--publicDir=./data/pb_public"],
    port: 8090,
    env: {},
    assets: [],
  },
  {
    id: "zeroclaw",
    name: "ZeroClaw",
    purpose: "Run a personal AI assistant with a web dashboard and chat integrations.",
    category: "AI assistants",
    projectUrl: "https://github.com/zeroclaw-labs/zeroclaw",
    version: "v0.9.0",
    releaseUrl: "https://github.com/zeroclaw-labs/zeroclaw/releases/download/v0.9.0/zeroclaw-x86_64-unknown-linux-musl.tar.gz",
    sha256: "8b9bc6735946158a8effbaa3c130efd8d4c1c983504cf20f08bb745af3544fd0",
    executable: "zeroclaw",
    args: ["daemon", "--host", "0.0.0.0", "--port", "42617"],
    port: 42617,
    env: {
      ZEROCLAW_CONFIG_DIR: "/app/data/zeroclaw",
      ZEROCLAW_DATA_DIR: "/app/data/zeroclaw",
    },
    assets: ["web/dist"],
  },
];

/** Same public entry point that upstream projects can link to from their sites. */
export function deployLink(recipe: DeployRecipe) {
  const params = new URLSearchParams({
    name: recipe.id,
    binary: recipe.releaseUrl,
    sha256: recipe.sha256,
    port: String(recipe.port),
    arg: JSON.stringify(recipe.args),
    minimal: "true",
  });
  return "/deploy?" + params;
}

type DeployLinkResult = { recipe?: DeployRecipe; error?: string };

export function readDeployLink(hash: string): DeployLinkResult {
  if (!hash.startsWith("#/deploy?")) return {};
  if (hash.length > 32768) return { error: "The deployment link is too long." };
  const params = new URLSearchParams(hash.slice("#/deploy?".length));
  const allowed = ["name", "binary", "sha256", "port", "arg", "minimal"];
  const seen = new Set<string>();
  for (const key of params.keys()) {
    if (!allowed.includes(key) || seen.has(key)) return { error: "The deployment link contains an unknown or repeated field." };
    seen.add(key);
  }

  const name = (params.get("name") ?? "").trim();
  if (!name || [...name].length > 80 || /\p{Cc}/u.test(name)) return { error: "Enter a valid app name in the deployment link." };
  const binary = params.get("binary") ?? "";
  let url: URL;
  try {
    url = new URL(binary);
  } catch {
    return { error: "Use an absolute HTTPS URL for the binary." };
  }
  if (url.protocol !== "https:" || url.username || url.password || url.hash || !binary.startsWith("https://")) {
    return { error: "Use an HTTPS binary URL without credentials or a fragment." };
  }
  const sha256 = (params.get("sha256") ?? "").toLowerCase();
  if (params.has("sha256") && !/^[a-f0-9]{64}$/.test(sha256)) return { error: "Use a 64-character SHA-256 checksum." };
  const portValue = params.get("port") ?? "8080";
  const port = Number(portValue);
  if (!/^[0-9]{1,5}$/.test(portValue) || port < 1 || port > 65535) return { error: "Use an HTTP port from 1 through 65535." };

  let args: unknown;
  try {
    args = JSON.parse(params.get("arg") ?? "[]");
  } catch {
    return { error: "Encode arg as a JSON array of strings." };
  }
  if (!Array.isArray(args) || args.length > 64 || !args.every((item): item is string => typeof item === "string" && !item.includes("\0"))) {
    return { error: "Use at most 64 string arguments without NUL characters." };
  }
  const encoder = new TextEncoder();
  if (args.reduce((sum, value) => sum + encoder.encode(value).length, 0) > 4096) {
    return { error: "The arguments exceed 4096 bytes." };
  }
  const minimal = params.get("minimal") ?? "false";
  if (minimal !== "true" && minimal !== "false") return { error: "Set minimal to true or false." };

  // Extra member paths, packaged assets and environment settings belong only to
  // the pinned archive. A link sharing its display name cannot inherit them.
  const pinned = deployRecipes.find(recipe => recipe.releaseUrl === binary && recipe.sha256 === sha256);
  return {
    recipe: {
      id: "external",
      purpose: "",
      category: "",
      projectUrl: url.origin,
      version: "",
      executable: "",
      env: {},
      assets: [],
      ...pinned,
      name: pinned?.id === name ? pinned.name : name,
      releaseUrl: binary,
      sha256,
      args,
      port,
      minimal: minimal === "true",
    },
  };
}
