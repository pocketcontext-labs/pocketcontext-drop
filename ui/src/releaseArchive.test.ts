// @vitest-environment node
import { expect, it } from "vitest";
import {
  BlobReader,
  BlobWriter,
  ZipWriter,
} from "@zip.js/zip.js/lib/zip-core-native.js";
import { digest, inspectArchive } from "./releaseArchive";
async function zip(names: string[], executable = true) {
  const writer = new ZipWriter(new BlobWriter(), {
    useWebWorkers: false,
    level: 0,
  });
  const header = new Uint8Array(128);
  if (executable) {
    header.set([0x7f, 69, 76, 70, 2, 1, 1]);
    header[16] = 2;
    header[18] = 62;
    const elf = new DataView(header.buffer);
    elf.setUint32(20, 1, true);
    elf.setBigUint64(24, 120n, true);
    elf.setBigUint64(32, 64n, true);
    elf.setUint16(52, 64, true);
    elf.setUint16(54, 56, true);
    elf.setUint16(56, 1, true);
    elf.setUint32(64, 1, true);
    elf.setUint32(68, 5, true);
    elf.setBigUint64(96, 128n, true);
    elf.setBigUint64(104, 128n, true);
  }
  for (const name of names)
    await writer.add(name, new BlobReader(new Blob([header])));
  return new File([await writer.close()], "test.zip");
}
it("verifies an archive once, reports every candidate and refuses an incorrect digest", async () => {
  const input = await zip(["one/bin", "two/bin"]);
  const info = await inspectArchive(input, await digest(input), () => {});
  expect(info.candidates).toEqual(["one/bin", "two/bin"]);
  await expect(inspectArchive(input, "0".repeat(64), () => {})).rejects.toThrow(
    /checksum mismatch/,
  );
});
it("refuses a ZIP without a compatible executable", async () => {
  await expect(
    inspectArchive(await zip(["readme"], false), "", () => {}),
  ).rejects.toThrow(/no compatible Linux/);
});
