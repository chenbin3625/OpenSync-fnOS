// 发布脚本自身的单元测试。覆盖那些出错代价高的逻辑：
// Node 运行时挑选（npm 的 PATH 依赖）、端口占用解析、manifest 版本读取。
import { test } from "node:test";
import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { PassThrough } from "node:stream";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, utimesSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pickNodeDir, root, MIN_NODE } from "./lib/toolchain.mjs";
import { ARCHES, buildFrontend, formatBytes, readVersion, withBuildLock } from "./lib/package.mjs";
import { parseManifestVersion, versionProblems } from "./lib/version.mjs";
import { createDevSupervisor } from "./dev.mjs";

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

test("pickNodeDir 优先尊重显式指定的 OPENSYNC_NODE_DIR", () => {
  const dir = mkdtempSync(join(tmpdir(), "opensync-node-override-"));
  const original = process.env.OPENSYNC_NODE_DIR;
  try {
    writeFileSync(join(dir, "node"), "#!/bin/sh\necho v22.12.0\n");
    writeFileSync(join(dir, "npm"), "#!/bin/sh\nexit 0\n");
    chmodSync(join(dir, "node"), 0o755);
    chmodSync(join(dir, "npm"), 0o755);
    process.env.OPENSYNC_NODE_DIR = dir;
    const picked = pickNodeDir();
    assert.equal(picked.dir, dir);
    assert.equal(picked.text, "v22.12.0");
    assert.equal(picked.satisfiesMinimum, true);
  } finally {
    if (original === undefined) delete process.env.OPENSYNC_NODE_DIR;
    else process.env.OPENSYNC_NODE_DIR = original;
    rmSync(dir, { recursive: true, force: true });
  }
});

test("parseManifestVersion 与 versionProblems 兼容 CRLF 并校验 X.Y.Z 格式", () => {
  assert.equal(parseManifestVersion("appname=opensync\r\nversion=0.0.39\r\n"), "0.0.39");
  const bad = versionProblems(
    {
      "fnos/manifest": "0.0.39.",
      "package.json": "0.0.39",
      "frontend/package.json": "0.0.39",
      "README.md": "0.0.39",
    },
    "v0.0.39",
  );
  assert.ok(bad.some((msg) => msg.includes("格式无效")));
  assert.ok(
    versionProblems(
      {
        "fnos/manifest": "0.0.39",
        "package.json": "0.0.39",
        "frontend/package.json": "0.0.39",
        "README.md": "0.0.39",
      },
      "v",
    ).some((msg) => msg.includes("Git tag")),
    "裸 v 标签必须被拒绝",
  );
});

test("withBuildLock 阻止并发打包并在异常后释放锁", () => {
  const lockDir = mkdtempSync(join(tmpdir(), "opensync-build-lock-"));
  try {
    assert.throws(
      () =>
        withBuildLock(() => {
          assert.throws(
            () => withBuildLock(() => {}, lockDir),
            /已有另一个打包进程正在运行/,
          );
          throw new Error("boom");
        }, lockDir),
      /boom/,
    );
    // 异常退出后锁必须已释放，允许后续打包继续。
    const result = withBuildLock(() => "ok", lockDir);
    assert.equal(result, "ok");
  } finally {
    rmSync(lockDir, { recursive: true, force: true });
  }
});

test("withBuildLock 不会删除刚创建的空锁文件，并能回收超时空锁与已退出进程锁", () => {
  const lockDir = mkdtempSync(join(tmpdir(), "opensync-build-lock-race-"));
  const lockPath = join(lockDir, ".build.lock");
  try {
    // 刚创建的空锁文件（模拟另一进程仍在初始化）不得被误删或抢占。
    writeFileSync(lockPath, "");
    assert.throws(
      () => withBuildLock(() => "should-not-run", lockDir),
      /已有另一个打包进程正在运行/,
    );
    assert.ok(existsSync(lockPath), "刚创建的空锁文件必须保持存在");

    // 超过宽限期的空锁文件视为异常残留，允许自动清理并重新加锁。
    const staleDate = new Date(Date.now() - 10_000);
    utimesSync(lockPath, staleDate, staleDate);
    assert.equal(withBuildLock(() => "recovered-empty", lockDir), "recovered-empty");
    assert.equal(existsSync(lockPath), false, "完成后锁文件应已清理");

    // 已退出 PID 留下的陈旧锁文件应自动清理并重新加锁。
    writeFileSync(lockPath, JSON.stringify({ pid: 999_999_999, startedAt: 1, token: "dead" }));
    assert.equal(withBuildLock(() => "recovered-dead-pid", lockDir), "recovered-dead-pid");
    assert.equal(existsSync(lockPath), false);
  } finally {
    rmSync(lockDir, { recursive: true, force: true });
  }
});

test("dev.mjs 在 spawn ENOENT 时不死锁并传播非零退出码", async () => {
  const logDir = mkdtempSync(join(tmpdir(), "opensync-dev-test-"));
  const exitCodes = [];
  const supervisor = createDevSupervisor({
    logDir,
    exitFn: (code) => exitCodes.push(code),
    fetchFn: async () => {
      throw new Error("unreachable");
    },
    log: () => {},
    stdout: { write: () => {} },
    pollIntervalMs: 10,
    stopGraceMs: 50,
  });
  try {
    const child = supervisor.start(
      "backend",
      "__opensync_nonexistent_binary_cmd__",
      [],
      { cwd: root, color: "36" },
    );
    await assert.rejects(
      () => supervisor.waitFor("backend", "http://127.0.0.1:1/test", child, 5000),
      /已退出|已中止/,
    );
    assert.deepEqual(exitCodes, [1]);
  } finally {
    rmSync(logDir, { recursive: true, force: true });
  }
});

test("dev.mjs shutdown 重入时等待进行中的收尾且 streamLogs 隔离 stdout/stderr 缓冲", async () => {
  const logDir = mkdtempSync(join(tmpdir(), "opensync-dev-stream-"));
  const exitCodes = [];
  const supervisor = createDevSupervisor({
    logDir,
    exitFn: (code) => exitCodes.push(code),
    killFn: () => {},
    log: () => {},
    stdout: { write: () => {} },
    stopGraceMs: 200,
  });
  try {
    const fakeChild = new EventEmitter();
    fakeChild.pid = 999999;
    fakeChild.exitCode = null;
    fakeChild.signalCode = null;
    fakeChild.kill = () => {};
    fakeChild.stdout = new PassThrough();
    fakeChild.stderr = new PassThrough();

    const logPath = join(logDir, "backend.log");
    supervisor.streamLogs(fakeChild, "backend", "36", logPath);
    supervisor.children.push({ name: "backend", child: fakeChild });

    fakeChild.stdout.write("alpha");
    fakeChild.stderr.write("err-one\nerr-two");
    fakeChild.stdout.end("-omega");
    fakeChild.stderr.end();

    const first = supervisor.shutdown(1, "first");
    const second = supervisor.shutdown(0, "second");
    // 在子进程尚未退出前，重入的 shutdown 不能提前完成。
    let secondResolved = false;
    second.then(() => {
      secondResolved = true;
    });
    await new Promise((r) => setTimeout(r, 30));
    assert.equal(secondResolved, false);

    fakeChild.exitCode = 0;
    fakeChild.emit("exit", 0, null);
    await Promise.all([first, second]);
    assert.equal(secondResolved, true);
    assert.deepEqual(exitCodes, [1]);

    const content = readFileSync(logPath, "utf8");
    assert.match(content, /^\[backend\] err-one$/m);
    assert.match(content, /^\[backend\] alpha-omega$/m);
    assert.match(content, /^\[backend\] err-two$/m);
  } finally {
    rmSync(logDir, { recursive: true, force: true });
  }
});

test("buildFrontend 强制覆盖 VITE_DATA_MODE=real，防止 mock 环境变量泄漏进生产包", () => {
  const dir = mkdtempSync(join(tmpdir(), "opensync-node-fe-"));
  const out = join(dir, "mode.txt");
  const prevMode = process.env.VITE_DATA_MODE;
  try {
    writeFileSync(join(dir, "node"), "#!/bin/sh\necho v22.12.0\n");
    writeFileSync(
      join(dir, "npm"),
      `#!/bin/sh\nprintf '%s' "$VITE_DATA_MODE" > "${out}"\n`,
    );
    chmodSync(join(dir, "node"), 0o755);
    chmodSync(join(dir, "npm"), 0o755);
    process.env.VITE_DATA_MODE = "mock";
    buildFrontend(dir);
    assert.equal(readFileSync(out, "utf8"), "real");
  } finally {
    if (prevMode === undefined) delete process.env.VITE_DATA_MODE;
    else process.env.VITE_DATA_MODE = prevMode;
    rmSync(dir, { recursive: true, force: true });
  }
});
