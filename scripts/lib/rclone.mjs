import { createHash } from "node:crypto";
import {
  chmodSync,
  closeSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  openSync,
  readFileSync,
  renameSync,
  rmSync,
} from "node:fs";
import { join } from "node:path";
import { root, run } from "./toolchain.mjs";

const version = "v1.76.0";
const hashes = {
  amd64: "b00f00b3266c3764413afe599d3ec316f6f43b3e0ea8a0bc1d186d2133e46df7",
  arm64: "ee169016675e23a69d77240565133ff7dbc9b33d16fa8f7053d4af39f1eaeb78",
};

export function bundleRclone(arch, target) {
  const cache =
    process.env.OPENSYNC_RCLONE_CACHE || join(root, ".tools", "rclone");
  mkdirSync(cache, { recursive: true });
  const stage = mkdtempSync(join(cache, ".download-"));
  const archive = `rclone-${version}-linux-${arch}`;
  function verified(name, url, hash) {
    const cached = join(cache, name);
    const file = existsSync(cached) ? cached : join(stage, name);
    if (file !== cached)
      run("curl", [
        "--fail",
        "--location",
        "--silent",
        "--show-error",
        "--retry",
        "2",
        "--output",
        file,
        url,
      ]);
    if (createHash("sha256").update(readFileSync(file)).digest("hex") !== hash)
      throw new Error(`rclone 校验失败：${name}`);
    if (file !== cached) renameSync(file, cached);
    return cached;
  }
  try {
    const zip = verified(
      archive + ".zip",
      `https://downloads.rclone.org/${version}/${archive}.zip`,
      hashes[arch],
    );
    const license = verified(
      "COPYING-" + version,
      `https://raw.githubusercontent.com/rclone/rclone/${version}/COPYING`,
      "8cd2e9e750b90a04b7d82dbbca3930c696ae0309d7c10464f90a44f45754cd04",
    );
    const bin = join(target, "rclone");
    const fd = openSync(bin, "w", 0o755);
    try {
      run("unzip", ["-p", zip, archive + "/rclone"], {
        stdio: ["ignore", fd, "inherit"],
      });
    } finally {
      closeSync(fd);
    }
    chmodSync(bin, 0o755);
    copyFileSync(license, join(target, "rclone-COPYING"));
  } finally {
    rmSync(stage, { recursive: true, force: true });
  }
}
