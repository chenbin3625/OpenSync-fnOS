#!/usr/bin/env node
// 单架构打包：node scripts/build.mjs amd64|arm64
// 双架构发布请用 scripts/release.mjs（它会复用这里同一套打包实现）。
import { ARCHES, buildFrontend, buildPackage } from "./lib/package.mjs";
import { pickNodeDir } from "./lib/toolchain.mjs";

const arch = process.argv[2] || "amd64";
if (!ARCHES.includes(arch))
  throw new Error(`架构必须是 ${ARCHES.join(" 或 ")}`);

const node = pickNodeDir();
buildFrontend(node.dir);
const file = buildPackage(arch, { nodeDir: node.dir });
console.log(`已生成 ${file}；发布前仍需在飞牛设备完成验收。`);
