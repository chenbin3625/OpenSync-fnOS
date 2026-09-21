// 飞牛 fpk 打包：前端构建一次 + 按架构交叉编译 Go 二进制 + fnpack 组装。
// build.mjs 与 release.mjs 共用这里的实现，避免两条打包路径产生差异。
import { createHash } from "node:crypto";
import {
  cpSync,
  existsSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  renameSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { join } from "node:path";
import { envWithNode, root, run } from "./toolchain.mjs";

/** 两个架构的 Go 交叉编译目标；飞牛 manifest 里 x86/arm 分别声明。 */
export const ARCHES = ["amd64", "arm64"];

const PLATFORM = { amd64: "x86", arm64: "arm" };

/** 打包前需要剔除的构建产物之外的杂项（macOS 会在目录里留下这些文件）。 */
const JUNK = new Set([".DS_Store"]);

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
  run("npm", ["run", "build"], {
    cwd: join(root, "frontend"),
    env: envWithNode(nodeDir),
  });
};

const resolveFnpack = () => {
  const candidate = process.env.FNPACK || join(root, ".tools", "fnpack");
  if (!existsSync(candidate))
    throw new Error(
      `找不到 fnpack：${candidate}\n按 README「飞牛打包」下载官方 fnpack 1.2.3 放到 .tools/fnpack，或用 FNPACK 指定路径。`,
    );
  return candidate;
};

/** 从 fnos/manifest 读取版本号，用于发布产物命名与追溯。 */
export const readVersion = () =>
  (
    /^version=(.+)$/m.exec(readFileSync(join(root, "fnos", "manifest"), "utf8")) ||
    []
  )[1];

/** 组装单个架构的 fpk，返回产物路径。 */
export const buildPackage = (arch, { nodeDir } = {}) => {
  if (!ARCHES.includes(arch)) throw new Error("架构必须是 amd64 或 arm64");
  const pack = join(root, "dist", `opensync-${arch}`);
  // 先清空，避免上一次构建的残留文件被打进包。
  rmSync(pack, { recursive: true, force: true });
  mkdirSync(pack, { recursive: true });
  copyTemplate(join(root, "fnos"), pack);
  cpSync(join(root, "LICENSE"), join(pack, "LICENSE"));
  const manifest = readFileSync(join(pack, "manifest"), "utf8").replace(
    /^platform=.+$/m,
    `platform=${PLATFORM[arch]}`,
  );
  writeFileSync(join(pack, "manifest"), manifest);

  mkdirSync(join(pack, "app", "server"), { recursive: true });
  run(
    "go",
    [
      "build",
      "-trimpath",
      `-ldflags=-s -w -X main.appVersion=${readVersion()}`,
      "-o",
      join(pack, "app", "server", "opensync"),
      "./cmd/server",
    ],
    {
      cwd: join(root, "backend"),
      env: { GOOS: "linux", GOARCH: arch, CGO_ENABLED: "0" },
    },
  );

  const fpk = join(root, "dist", `opensync-${arch}.fpk`);
  rmSync(fpk, { force: true });
  run(resolveFnpack(), ["build", "--directory", pack], {
    cwd: join(root, "dist"),
  });
  renameSync(join(root, "dist", "opensync.fpk"), fpk);
  return fpk;
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
  const lines = files.map((file) => `${sha256(file)}  ${file.split("/").pop()}`);
  const target = join(root, "dist", "SHA256SUMS");
  writeFileSync(
    target,
    `# OpenSync ${version} 飞牛安装包校验和\n${lines.join("\n")}\n`,
  );
  return target;
};
