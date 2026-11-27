import { BlobReader, ZipReader } from "@zip.js/zip.js/lib/zip-core-native.js";
import { MAX_RELEASE_BYTES, releaseFileProblem } from "./dropApi";

const expandedLimit = 384 * 1024 * 1024;
const entryLimit = 4096;
export type Archive = {
  digest: string;
  files: Map<string, Blob>;
  candidates: string[];
};
export const archiveName = (name: string) => /\.(zip|tar\.gz|tgz)$/i.test(name);
export async function digest(blob: Blob) {
  return Array.from(
    new Uint8Array(
      await crypto.subtle.digest("SHA-256", await blob.arrayBuffer()),
    ),
    (byte) => byte.toString(16).padStart(2, "0"),
  ).join("");
}
function pathName(raw: string) {
  const name = raw.replace(/^(\.\/)+/, "").replace(/\/$/, "");
  if (
    !name ||
    name.length > 1024 ||
    /[\\:\x00-\x1f]/.test(name) ||
    name.split("/").some((part) => !part || part === "." || part === "..")
  ) {
    throw Error("The archive contains an unsafe file path.");
  }
  return name;
}
function boundedWriter(max: number) {
  const chunks: BlobPart[] = [];
  let size = 0;
  const stream = new WritableStream<Uint8Array>({
    write(bytes) {
      size += bytes.byteLength;
      if (size > max)
        throw Error("The archive exceeds the expanded-size limit.");
      chunks.push(new Uint8Array(bytes));
    },
  });
  return { stream, blob: () => new Blob(chunks) };
}
async function unpackZip(
  input: Blob,
  add: (path: string, content?: Blob) => void,
) {
  const reader = new ZipReader(new BlobReader(input), {
    useWebWorkers: false,
    useCompressionStream: true,
  });
  let remaining = expandedLimit;
  try {
    for await (const entry of reader.getEntriesGenerator()) {
      const kind =
        (entry.unixMode ?? entry.externalFileAttributes >>> 16) & 0xf000;
      if (entry.encrypted || (kind && kind !== 0x8000 && kind !== 0x4000))
        throw Error("The ZIP must contain only files and directories.");
      if (entry.directory) {
        add(entry.filename);
        continue;
      }
      if (entry.uncompressedSize > remaining)
        throw Error("The archive exceeds the expanded-size limit.");
      const writer = boundedWriter(Math.min(entry.uncompressedSize, remaining));
      await entry.getData(writer.stream, { checkSignature: true });
      const blob = writer.blob();
      if (blob.size !== entry.uncompressedSize)
        throw Error("A ZIP member has the wrong length.");
      remaining -= blob.size;
      add(entry.filename, blob);
    }
  } finally {
    await reader.close();
  }
}
async function unpackTar(
  input: Blob,
  add: (path: string, content?: Blob) => void,
) {
  const writer = boundedWriter(expandedLimit);
  await input
    .stream()
    .pipeThrough(new DecompressionStream("gzip"))
    .pipeTo(writer.stream);
  const tar = writer.blob();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  for (let pos = 0; pos + 512 <= tar.size;) {
    const header = new Uint8Array(
      await tar.slice(pos, pos + 512).arrayBuffer(),
    );
    if (header.every((byte) => !byte)) return;
    function field(start: number, end: number) {
      const bytes = header.subarray(start, end);
      const nul = bytes.indexOf(0);
      return decoder.decode(nul < 0 ? bytes : bytes.subarray(0, nul));
    }
    const octal = (start: number, end: number) => {
      const value = field(start, end).trim();
      if (!/^[0-7]+$/.test(value)) throw Error("The tar header is invalid.");
      return parseInt(value, 8);
    };
    if (
      header.reduce((sum, v, i) => sum + (i >= 148 && i < 156 ? 32 : v), 0) !==
      octal(148, 156)
    )
      throw Error("The tar header checksum is invalid.");
    const size = octal(124, 136);
    const path = [field(345, 500), field(0, 100)].filter(Boolean).join("/");
    pos += 512;
    if (pos + size > tar.size) throw Error("The tar archive is incomplete.");
    if (header[156] === 53 && size === 0) add(path);
    else if (header[156] === 0 || header[156] === 48)
      add(path, tar.slice(pos, pos + size));
    else
      throw Error("The tar must contain only regular files and directories.");
    pos += Math.ceil(size / 512) * 512;
  }
  throw Error("The tar archive is incomplete.");
}

// Mirror the loader-facing checks in dropapi.ReleaseImageProblem so selection
// counts only runnable candidates. The guest repeats the shared Go check.
export async function standaloneELF(file: Blob) {
  if (file.size < 64 || (await releaseFileProblem(file))) return false;
  const header = new DataView(await file.slice(0, 64).arrayBuffer());
  const count = header.getUint16(56, true);
  const start = Number(header.getBigUint64(32, true));
  if (
    header.getUint32(20, true) !== 1 ||
    header.getUint16(52, true) !== 64 ||
    header.getUint16(54, true) !== 56 ||
    !count ||
    count * 56 > 65536 ||
    start < 64 ||
    start + count * 56 > file.size
  )
    return false;
  const segments = new DataView(
    await file.slice(start, start + count * 56).arrayBuffer(),
  );
  const entry = header.getBigUint64(24, true);
  let hasEntry = false,
    hasDynamic = false;
  for (let i = 0; i < count * 56; i += 56) {
    const type = segments.getUint32(i, true);
    if (type === 3) return false; // PT_INTERP
    if (type !== 1 && type !== 2) continue;
    const offset = segments.getBigUint64(i + 8, true);
    const address = segments.getBigUint64(i + 16, true);
    const size = segments.getBigUint64(i + 32, true);
    if (
      offset + size > BigInt(file.size) ||
      size > segments.getBigUint64(i + 40, true)
    )
      return false;
    if (
      type === 1 &&
      segments.getUint32(i + 4, true) & 1 &&
      entry !== 0n &&
      entry >= address &&
      entry - address < size
    )
      hasEntry = true;
    if (type === 2) {
      if (hasDynamic) return false;
      hasDynamic = true;
      const dynamic = new DataView(
        await file.slice(Number(offset), Number(offset + size)).arrayBuffer(),
      );
      let end = false;
      for (let d = 0; d + 16 <= dynamic.byteLength; d += 16) {
        const tag = dynamic.getBigUint64(d, true);
        if (tag === 1n) return false; // DT_NEEDED
        if (tag === 0n) {
          end = true;
          break;
        }
      }
      if (!end) return false;
    }
  }
  return hasEntry;
}
export async function inspectArchive(
  input: File,
  expected: string,
  progress: (s: string) => void,
): Promise<Archive> {
  if (input.size > MAX_RELEASE_BYTES)
    throw Error("Choose an archive of 90 MiB or less.");
  progress("Verifying archive checksum…");
  const hash = await digest(input);
  if (expected && hash !== expected.toLowerCase())
    throw Error(
      "Archive checksum mismatch. Check the expected SHA-256 or choose the original release.",
    );
  progress("Extracting and checking archive members…");
  const files = new Map<string, Blob>();
  const seen = new Set<string>();
  let count = 0;
  function add(raw: string, content?: Blob) {
    if (++count > entryLimit)
      throw Error("The archive contains too many entries.");
    if ((raw === "./" || raw === ".") && !content) return;
    const name = pathName(raw);
    if (seen.has(name)) throw Error("The archive contains duplicate paths.");
    seen.add(name);
    if (content) files.set(name, content);
  }
  const magic = new Uint8Array(await input.slice(0, 4).arrayBuffer());
  if (magic[0] === 0x50 && magic[1] === 0x4b) await unpackZip(input, add);
  else if (magic[0] === 0x1f && magic[1] === 0x8b) await unpackTar(input, add);
  else throw Error("Choose a ZIP or gzip-compressed tar archive.");
  for (const name of seen) {
    const parts = name.split("/");
    while (parts.length > 1) {
      parts.pop();
      if (files.has(parts.join("/")))
        throw Error("An archive member is both a file and a directory.");
    }
  }
  const candidates: string[] = [];
  for (const [name, blob] of files) {
    if (await standaloneELF(blob)) candidates.push(name);
  }
  if (!candidates.length)
    throw Error(
      "This archive contains no compatible Linux (amd64) executable of 90 MiB or less.",
    );
  return { digest: hash, files, candidates };
}

export type LaunchOptions = {
  executable: string;
  args: string[];
  env: Record<string, string>;
  assets: string[];
  seed: string;
};
export async function bundleRelease(
  archive: Archive,
  opts: LaunchOptions,
): Promise<File> {
  const executable = archive.files.get(opts.executable);
  if (!executable || !archive.candidates.includes(opts.executable))
    throw Error("Choose a compatible executable.");
  const required = [...opts.assets, ...(opts.seed ? [opts.seed] : [])];
  const files = [...archive.files].filter(
    ([path]) =>
      path === opts.executable ||
      required.some(
        (prefix) => path === prefix || path.startsWith(prefix + "/"),
      ),
  );
  if (files.some(([p]) => p === "data" || p.startsWith("data/")))
    throw Error(
      "The packaged path data is reserved for the persistent volume.",
    );
  for (const p of required) {
    pathName(p);
    if (!files.some(([path]) => path === p || path.startsWith(p + "/")))
      throw Error("Missing archive path: " + p);
  }
  if (opts.seed && !files.some(([p]) => p.startsWith(opts.seed + "/")))
    throw Error("The initial data path must be a directory.");
  const manifest = new TextEncoder().encode(
    JSON.stringify({
      archiveSHA256: archive.digest,
      executableSHA256: await digest(executable),
      ...opts,
      files: files.map(([path, blob]) => ({ path, size: blob.size })),
    }),
  );
  if (manifest.length > 1024 * 1024)
    throw Error("The launch settings are too large.");
  const prefix = new DataView(new ArrayBuffer(4));
  prefix.setUint32(0, manifest.length, true);
  const payload = new Blob([
    prefix.buffer,
    manifest,
    ...files.map(([, blob]) => blob),
  ]);
  const compressed = await new Response(
    payload.stream().pipeThrough(new CompressionStream("gzip")),
  ).blob();
  const trailer = new DataView(new ArrayBuffer(8));
  trailer.setUint32(0, compressed.size, true);
  trailer.setUint32(4, 0x44525031, false);
  const { default: base64 } = await import("virtual:drop-guest");
  const launcher = Uint8Array.from(atob(base64), (c) => c.charCodeAt(0));
  const result = new File(
    [launcher, compressed, trailer.buffer],
    opts.executable.split("/").pop()!,
  );
  if (result.size > MAX_RELEASE_BYTES)
    throw Error("The packaged release exceeds 90 MiB. Select fewer assets.");
  return result;
}
