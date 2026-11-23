#!/usr/bin/env node
// 单架构打包：node scripts/build.mjs amd64|arm64
// 双架构发布请用 scripts/release.mjs（它会复用这里同一套打包实现）。
import {
  ARCHES,
  buildFrontend,
  buildPackage,
  withBuildLock,
} from "./lib/package.mjs";
import { MIN_NODE, pickNodeDir } from "./lib/toolchain.mjs";
import { collectVersions, versionProblems } from "./lib/version.mjs";

const arch = process.argv[2] || "amd64";
if (!ARCHES.includes(arch))
  throw new Error(`架构必须是 ${ARCHES.join(" 或 ")}`);

const node = pickNodeDir();
if (!node.satisfiesMinimum)
  throw new Error(`Node ${node.text} 低于最低要求 v${MIN_NODE.join(".")}`);
const versionIssues = versionProblems(collectVersions());
if (versionIssues.length > 0)
  throw new Error(`版本一致性检查失败：${versionIssues.join("；")}`);
const file = withBuildLock(() => {
  buildFrontend(node.dir);
  return buildPackage(arch, { nodeDir: node.dir });
});
console.log(`已生成 ${file}；发布前仍需在飞牛设备完成验收。`);
