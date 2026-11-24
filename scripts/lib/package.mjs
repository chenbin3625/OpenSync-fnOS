// 飞牛 fpk 打包：前端构建一次 + 按架构交叉编译 Go 二进制 + fnpack 组装。
// build.mjs 与 release.mjs 共用这里的实现，避免两条打包路径产生差异。
import { createHash, randomBytes } from "node:crypto";
import {
  closeSync,
  cpSync,
  existsSync,
  linkSync,
  mkdirSync,
  mkdtempSync,
  openSync,
  readFileSync,
  readdirSync,
  renameSync,
  rmSync,
  statSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { basename, join, resolve } from "node:path";
import { envWithNode, root, run } from "./toolchain.mjs";
import { parseManifestVersion, SEMVER_PATTERN } from "./version.mjs";

/** 两个架构的 Go 交叉编译目标；飞牛 manifest 里 x86/arm 分别声明。 */
export const ARCHES = ["amd64", "arm64"];

const PLATFORM = { amd64: "x86", arm64: "arm" };

/** 打包前需要剔除的构建产物之外的杂项（macOS 会在目录里留下这些文件）。 */
const JUNK = new Set([".DS_Store"]);

const isProcessAlive = (pid) => {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return error?.code !== "ESRCH";
  }
};

const UNINITIALIZED_LOCK_GRACE_MS = 5000;

const publishLockFile = (lockDir, lockPath, payload, token) => {
  const tmpPath = join(lockDir, `.build.lock.tmp.${process.pid}.${token}`);
  writeFileSync(tmpPath, payload, { flag: "wx", mode: 0o600 });
  try {
    try {
      // linkSync publishes the already-populated file atomically with EEXIST
      // semantics, so lockPath never appears empty between open and write.
      linkSync(tmpPath, lockPath);
    } catch (error) {
      if (
        error?.code === "EPERM" ||
        error?.code === "ENOTSUP" ||
        error?.code === "EXDEV"
      ) {
        const fd = openSync(lockPath, "wx", 0o600);
        try {
          writeFileSync(fd, payload);
        } finally {
          closeSync(fd);
        }
        return;
      }
      throw error;
    }
  } finally {
    rmSync(tmpPath, { force: true });
  }
};

/** 串行化打包过程，避免并发构建同时改写 backend/cmd/server/web 或 dist 产物。 */
export const withBuildLock = (fn, lockDir = join(root, "dist")) => {
  mkdirSync(lockDir, { recursive: true });
  const lockPath = join(lockDir, ".build.lock");
  const token = randomBytes(8).toString("hex");
  const payload = JSON.stringify({
    pid: process.pid,
    startedAt: Date.now(),
    token,
  });
  let acquired = false;
  for (let attempt = 0; attempt < 2; attempt += 1) {
    try {
      publishLockFile(lockDir, lockPath, payload, token);
      acquired = true;
      break;
    } catch (error) {
      if (error?.code !== "EEXIST") throw error;
      let existingPid = null;
      let lockAgeMs = 0;
      let lockMissing = false;
      try {
        const stat = statSync(lockPath);
        lockAgeMs = Math.max(0, Date.now() - stat.mtimeMs);
        const parsed = JSON.parse(readFileSync(lockPath, "utf8"));
        const pid = Number(parsed?.pid);
        if (Number.isInteger(pid) && pid > 0) {
          existingPid = pid;
        }
      } catch (readErr) {
        if (readErr?.code === "ENOENT") {
          lockMissing = true;
        }
      }
      if (lockMissing) {
        continue;
      }
      if (existingPid !== null) {
        if (!isProcessAlive(existingPid)) {
          rmSync(lockPath, { force: true });
          continue;
        }
        throw new Error(
          `已有另一个打包进程正在运行（PID ${existingPid}），请等待其完成后重试`,
        );
      }
      // Unparseable or empty lock file: do not delete a freshly created lock
      // that another process may still be initializing.
      if (lockAgeMs >= UNINITIALIZED_LOCK_GRACE_MS) {
        rmSync(lockPath, { force: true });
        continue;
      }
      throw new Error("已有另一个打包进程正在运行，请等待其完成后重试");
    }
  }
  if (!acquired) {
    throw new Error("无法获取打包锁，请稍后重试");
  }
  const release = () => {
    if (!acquired) return;
    acquired = false;
    try {
      const current = JSON.parse(readFileSync(lockPath, "utf8"));
      if (current?.pid === process.pid && current?.token === token) {
        unlinkSync(lockPath);
      }
    } catch {
      /* 已清理或不存在 */
    }
  };
  const onSignal = (signal) => {
    release();
    process.kill(process.pid, signal);
  };
  process.once("SIGINT", onSignal);
  process.once("SIGTERM", onSignal);
  try {
    return fn();
  } finally {
    process.off("SIGINT", onSignal);
    process.off("SIGTERM", onSignal);
    release();
  }
};

const copyTemplate = (from, to) => {
  for (const entry of readdirSync(from, { withFileTypes: true })) {
    if (JUNK.has(entry.name)) continue;
    const source = join(from, entry.name);
    const target = join(to, entry.name);
    if (entry.isDirectory()) {
      mkdirSync(target, { recursive: true });
      copyTemplate(source, target);
    } else if (entry.isSymbolicLink()) {
      cpSync(source, target);
    } else if (entry.isFile()) {
      cpSync(source, target);
    }
  }
};

export const requireNodeModules = (nodeDir, { install = true } = {}) => {
  const directory = join(root, "frontend", "node_modules");
  if (existsSync(directory)) return false;
  if (!install)
    throw new Error("frontend/node_modules 不存在，请先运行 npm ci");
  console.log("frontend/node_modules 不存在，先执行 npm ci…");
  run("npm", ["ci"], {
    cwd: join(root, "frontend"),
    env: envWithNode(nodeDir),
  });
  return true;
};

export const buildFrontend = (nodeDir) => {
  requireNodeModules(nodeDir);
  // 强制覆盖 VITE_DATA_MODE，防止宿主环境变量中的 VITE_DATA_MODE=mock
  // 把 mockServiceWorker.js 打进生产发布包。
  run("npm", ["run", "build"], {
    cwd: join(root, "frontend"),
    env: { ...envWithNode(nodeDir), VITE_DATA_MODE: "real" },
  });
};

const resolveFnpack = () => {
  const candidate = resolve(
    process.cwd(),
    process.env.FNPACK || join(root, ".tools", "fnpack"),
  );
  if (!existsSync(candidate))
    throw new Error(
      `找不到 fnpack：${candidate}\n按 README「飞牛打包」下载官方 fnpack 1.2.3 放到 .tools/fnpack，或用 FNPACK 指定路径。`,
    );
  return candidate;
};

/** 从 fnos/manifest 读取版本号，用于发布产物命名与追溯。 */
export const readVersion = () =>
  parseManifestVersion(readFileSync(join(root, "fnos", "manifest"), "utf8"));

/** 组装单个架构的 fpk，返回产物路径。 */
export const buildPackage = (arch, { nodeDir } = {}) => {
  if (!ARCHES.includes(arch)) throw new Error("架构必须是 amd64 或 arm64");
  // The version is injected into the binary. Building without it used to ship
  // appVersion="undefined" from a manifest typo, and a release that reports the
  // wrong version cannot be matched to its source.
  const version = readVersion();
  if (!version || !SEMVER_PATTERN.test(version))
    throw new Error(
      `fnos/manifest 缺少有效的 version=（当前：${version || "空"}），无法注入应用版本`,
    );
  const distDir = join(root, "dist");
  mkdirSync(distDir, { recursive: true });
  const stage = mkdtempSync(join(distDir, `.stage-${arch}-`));
  const pack = join(stage, `opensync-${arch}`);
  const fpk = join(distDir, `opensync-${arch}.fpk`);
  // 清理旧的残留暂存目录或目标 fpk，避免失败后留下半成品/旧包。
  rmSync(join(distDir, `opensync-${arch}`), { recursive: true, force: true });
  rmSync(fpk, { force: true });
  try {
    mkdirSync(pack, { recursive: true });
    copyTemplate(join(root, "fnos"), pack);
    cpSync(join(root, "LICENSE"), join(pack, "LICENSE"));
    const manifest = readFileSync(join(pack, "manifest"), "utf8")
      .replace(/\r\n/g, "\n")
      .replace(/\r/g, "\n")
      .replace(/^platform=.+$/m, `platform=${PLATFORM[arch]}`);
    writeFileSync(join(pack, "manifest"), manifest);

    mkdirSync(join(pack, "app", "server"), { recursive: true });
    run(
      "go",
      [
        "build",
        "-trimpath",
        // VCS stamping embeds the commit and a dirty flag, so the same source
        // produced different binaries from different checkouts. -trimpath alone
        // does not remove it.
        "-buildvcs=false",
        `-ldflags=-s -w -X main.appVersion=${version}`,
        "-o",
        join(pack, "app", "server", "opensync"),
        "./cmd/server",
      ],
      {
        cwd: join(root, "backend"),
        env: { GOOS: "linux", GOARCH: arch, CGO_ENABLED: "0" },
      },
    );

    run(resolveFnpack(), ["build", "--directory", pack], {
      cwd: stage,
    });
    renameSync(join(stage, "opensync.fpk"), fpk);
    return fpk;
  } catch (error) {
    rmSync(fpk, { force: true });
    throw error;
  } finally {
    rmSync(stage, { recursive: true, force: true });
  }
};

export const sha256 = (file) =>
  createHash("sha256").update(readFileSync(file)).digest("hex");

export const fileSize = (file) => statSync(file).size;

export const formatBytes = (bytes) => {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KiB", "MiB", "GiB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(1)} ${units[unit]}`;
};

/** 写出 dist/SHA256SUMS，可用 shasum -a 256 -c 校验。 */
export const writeChecksums = (files, version) => {
  const lines = files.map((file) => `${sha256(file)}  ${basename(file)}`);
  const target = join(root, "dist", "SHA256SUMS");
  writeFileSync(
    target,
    `# OpenSync ${version} 飞牛安装包校验和\n${lines.join("\n")}\n`,
  );
  return target;
};
