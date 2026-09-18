// 发布脚本自身的单元测试。覆盖那些出错代价高的逻辑：
// Node 运行时挑选（npm 的 PATH 依赖）、端口占用解析、manifest 版本读取。
import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { pickNodeDir, root, MIN_NODE } from "./lib/toolchain.mjs";
import { ARCHES, formatBytes, readVersion } from "./lib/package.mjs";

test("pickNodeDir 选出满足最低版本要求的 Node，且目录同时含 npm", () => {
  const picked = pickNodeDir();
  // 目录里必须有 npm，否则前置 PATH 修不好 npm 的运行时（npm 的 shebang 靠 PATH 找 node）。
  assert.ok(picked.dir, "必须返回运行时目录");
  assert.match(picked.text, /^v\d+\.\d+\.\d+$/);
  assert.ok(
    ["node", "npm"].every((binary) => existsSync(join(picked.dir, binary))),
    "目录内必须同时存在 node 与 npm",
  );
  // 只要环境里存在满足要求的 Node，就必须选中满足要求的那一个。
  if (picked.satisfiesMinimum) {
    const [major, minor, patch] = picked.version;
    const minimum = MIN_NODE;
    assert.ok(
      major > minimum[0] ||
        (major === minimum[0] && minor > minimum[1]) ||
        (major === minimum[0] && minor === minimum[1] && patch >= minimum[2]),
      `${picked.text} 应满足最低版本 v${MIN_NODE.join(".")}`,
    );
  }
});

test("readVersion 从 fnos/manifest 读出可用的版本号", () => {
  const version = readVersion();
  assert.ok(version, "manifest 必须声明 version");
  assert.match(version, /^\d+\.\d+\.\d+$/);
  // 与 manifest 原文一致，避免正则误匹配到别的行。
  assert.match(
    readFileSync(join(root, "fnos", "manifest"), "utf8"),
    new RegExp(`^version=${version.replace(/\./g, "\\.")}$`, "m"),
  );
});

test("ARCHES 与 fnos/manifest 的平台声明一致", () => {
  assert.deepEqual(ARCHES, ["amd64", "arm64"]);
  const manifest = readFileSync(join(root, "fnos", "manifest"), "utf8");
  // 模板声明 x86，build 阶段会替换；两个架构都必须有对应的 platform 取值。
  assert.match(manifest, /^platform=(x86|arm)$/m);
});

test("formatBytes 输出人类可读的产物大小", () => {
  assert.equal(formatBytes(512), "512 B");
  assert.equal(formatBytes(2048), "2.0 KiB");
  assert.equal(formatBytes(10 * 1024 * 1024), "10.0 MiB");
});
