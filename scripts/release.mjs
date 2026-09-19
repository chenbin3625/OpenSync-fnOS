#!/usr/bin/env node
// 一键发布：校验 → 全量测试 → 前端构建一次 → 双架构打包 → 校验和与产物清单 → 推送 GitHub。
// 用法：node scripts/release.mjs [--skip-tests]
import { existsSync } from "node:fs";
import { join, relative } from "node:path";
import {
  capture,
  envWithNode,
  pickNodeDir,
  root,
  run,
} from "./lib/toolchain.mjs";
import {
  ARCHES,
  buildFrontend,
  buildPackage,
  fileSize,
  formatBytes,
  readVersion,
  requireNodeModules,
  sha256,
  writeChecksums,
} from "./lib/package.mjs";

const argv = process.argv.slice(2);
if (argv.includes("--help") || argv.includes("-h")) {
  console.log(`一键发布飞牛安装包。

  node scripts/release.mjs [--skip-tests]

依次执行：工作区检查 → 前端类型检查与单元测试 → Go 测试 →
前端构建 → 交叉编译 amd64 / arm64 → fnpack 打包 → 输出校验和 → 推送 GitHub。

  --skip-tests   跳过前后端测试（仅构建，出包更快）
`);
  process.exit(0);
}
const runTests = !argv.includes("--skip-tests");

const step = (index, total, text) =>
  console.log(`\n\x1b[1m[${index}/${total}] ${text}\x1b[0m`);
const TOTAL = 7;

// ── 1. 工作区状态 ────────────────────────────────────────────────────────
step(1, TOTAL, "检查工作区状态");
const status = capture("git", ["status", "--porcelain"]).stdout || "";
const dirty = status
  .split("\n")
  .map((line) => line.trim())
  .filter(Boolean);
if (dirty.length > 0) {
  console.warn("\x1b[33m⚠ 工作区存在未提交改动，产物无法用提交号追溯：\x1b[0m");
  for (const line of dirty) console.warn(`    ${line}`);
  console.warn(
    "\x1b[33m  建议先提交，或在发布说明中记录对应提交。\x1b[0m",
  );
}
const head = (capture("git", ["rev-parse", "--short", "HEAD"]).stdout || "").trim();
console.log(`HEAD ${head}；工作区${dirty.length === 0 ? "干净" : "有改动"}`);

// ── 2. Node 与依赖 ───────────────────────────────────────────────────────
step(2, TOTAL, "准备 Node 运行时与依赖");
const node = pickNodeDir();
if (!node.satisfiesMinimum)
  throw new Error(
    `Node ${node.text} 低于 README 要求的 v22.12.0，前端构建不可靠。可用 OPENSYNC_NODE_DIR 指定运行时目录。`,
  );
console.log(`Node ${node.text}（${node.dir}，已前置 PATH）`);
requireNodeModules(node.dir);

// ── 3. 测试 ──────────────────────────────────────────────────────────────
step(3, TOTAL, runTests ? "运行前后端测试" : "跳过测试（--skip-tests）");
if (runTests) {
  // 只做类型检查与单测；Vite 只剥离类型不检查，所以 tsc 必须单独跑。
  console.log("· 前端：tsc --noEmit");
  run("npx", ["tsc", "--noEmit"], {
    cwd: join(root, "frontend"),
    env: envWithNode(node.dir),
  });
  console.log("· 前端：vitest");
  run("npm", ["test"], {
    cwd: join(root, "frontend"),
    env: envWithNode(node.dir),
  });
  console.log("· Go：go test ./...");
  run("go", ["test", "./..."], { cwd: join(root, "backend") });
}

// ── 4. 前端构建 ──────────────────────────────────────────────────────────
step(4, TOTAL, "构建前端（仅一次，两个架构共用）");
buildFrontend(node.dir);

// ── 5. 双架构打包 ────────────────────────────────────────────────────────
step(5, TOTAL, `打包 ${ARCHES.join(" / ")}`);
const version = readVersion();
if (!version) throw new Error("无法从 fnos/manifest 读取 version");
const artifacts = [];
for (const arch of ARCHES)
  artifacts.push({ arch, file: buildPackage(arch, { nodeDir: node.dir }) });

// ── 6. 校验和与产物清单 ──────────────────────────────────────────────────
step(6, TOTAL, "生成 SHA256SUMS 与产物清单");
const checksumFile = writeChecksums(
  artifacts.map(({ file }) => file),
  version,
);

console.log(`\n\x1b[32m✓ 发布产物已生成\x1b[0m  OpenSync ${version}  HEAD ${head}`);
for (const { arch, file } of artifacts)
  console.log(
    `  ${arch.padEnd(6)} ${formatBytes(fileSize(file)).padStart(10)}  ${sha256(file)}  ${relative(root, file)}`,
  );
console.log(`  校验和  ${relative(root, checksumFile)}`);

// 本机平台不匹配 ARM，无法执行产物，故只做存在性提醒。
if (!existsSync(artifacts[0].file)) throw new Error("产物缺失");

// ── 7. 推送 GitHub ──────────────────────────────────────────────────────
step(7, TOTAL, "推送到 GitHub");
const remote = (capture("git", ["remote", "get-url", "origin"]).stdout || "").trim();
if (!remote) {
  console.warn("\x1b[33m⚠ 未配置 origin 远程仓库，跳过推送。\x1b[0m");
} else {
  console.log(`远程仓库：${remote}`);
  run("git", ["push", "origin", "HEAD"]);
  // 推送版本 tag
  const tag = `v${version}`;
  const tagExists = capture("git", ["tag", "-l", tag]).stdout.trim() === tag;
  if (!tagExists) {
    run("git", ["tag", tag]);
    console.log(`已创建 tag ${tag}`);
  }
  run("git", ["push", "origin", tag]);
  console.log(`已推送 ${tag} 到 ${remote}`);
}

console.log(`
────────────────────────────────────────────────────────
  打包成功 \x1b[1m不等于可以发布\x1b[0m。README「设备验收」要求在真实飞牛设备确认：
    · 两种架构的安装 / 升级 / 启动 / 停止 / 重启与数据保留
    · 统一网关会话、管理员与普通用户隔离、Socket 权限、SSE 长连接
    · 桌面 iframe 与移动 App 的 SDK 初始化、宿主主题、目录授权回调
    · 真实 OpenList / AList 驱动的三种同步模式与五种通知
────────────────────────────────────────────────────────
`);
