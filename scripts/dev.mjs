#!/usr/bin/env node
// 一键启动本地预览：Go 后端（--dev，仅监听回环）+ Vite 前端开发服务。
// 用法：node scripts/dev.mjs  或  npm run dev
import { spawn } from "node:child_process";
import { createWriteStream, mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import {
  envWithNode,
  pickNodeDir,
  portOccupancy,
  describePid,
  readFrontendConfig,
  root,
  MIN_NODE,
} from "./lib/toolchain.mjs";
import { requireNodeModules } from "./lib/package.mjs";

// 端口来自 vite.config.ts：代理目标即后端端口，server.port 即前端端口。
// 脚本不重复声明，改了配置这里自动跟随。
const { frontendPort: FRONTEND_PORT, backendPort } = readFrontendConfig();
if (!backendPort)
  throw new Error("vite.config.ts 未声明后端代理目标，无法确定后端端口");
const BACKEND_PORT = backendPort;
const BASE_PATH = "/app/opensync";

const backendUrl = `http://127.0.0.1:${BACKEND_PORT}${BASE_PATH}/svr/session`;
const frontendUrl = `http://127.0.0.1:${FRONTEND_PORT}${BASE_PATH}/`;

const argv = process.argv.slice(2);
if (argv.includes("--help") || argv.includes("-h")) {
  console.log(`一键启动前后端预览。

  node scripts/dev.mjs [--no-install]

前端 http://127.0.0.1:${FRONTEND_PORT}${BASE_PATH}/  后端 http://127.0.0.1:${BACKEND_PORT}
端口取自 vite.config.ts；改端口请改配置，脚本会自动跟随。

  --no-install   缺少 frontend/node_modules 时直接报错，不自动执行 npm ci
`);
  process.exit(0);
}
const autoInstall = !argv.includes("--no-install");

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// ── 1. Node 运行时：npm 的 shebang 通过 PATH 找 node，必须前置目录 ──────────
const node = pickNodeDir();
console.log(
  `Node 运行时：\x1b[36m${node.text}\x1b[0m（${node.dir}，已前置 PATH）`,
);
if (!node.satisfiesMinimum)
  console.warn(
    `\x1b[33m⚠ Node ${node.text} 低于 README 要求的 v${MIN_NODE.join(".")}，Vite 可能告警或失败。\x1b[0m`,
  );

// ── 2. 端口占用：多会话环境下不擅自结束别人的进程 ─────────────────────────
const conflicts = [];
for (const [port, label] of [
  [BACKEND_PORT, "后端"],
  [FRONTEND_PORT, "前端"],
]) {
  const occupancy = await portOccupancy(port);
  if (occupancy.busy) conflicts.push({ port, label, ...occupancy });
}
if (conflicts.length > 0) {
  console.error("\n\x1b[31m✗ 端口已被占用，未启动任何服务。\x1b[0m\n");
  for (const conflict of conflicts) {
    console.error(`  ${conflict.label}端口 ${conflict.port}`);
    if (conflict.pids.length > 0) {
      for (const pid of conflict.pids)
        console.error(`    PID ${pid}  ${describePid(pid)}`);
      console.error(
        `    停止：kill ${conflict.pids.join(" ")}   （或停掉启动它的那个会话）`,
      );
    } else {
      console.error("    无法取得 PID（lsof 不可用），请自行定位占用进程");
    }
    console.error("");
  }
  console.error(
    "本项目为多会话环境，其他会话可能正在使用这些端口。确认可以结束后再重试。",
  );
  process.exit(1);
}

// ── 3. 依赖与日志目录 ────────────────────────────────────────────────────
requireNodeModules(node.dir, { install: autoInstall });
const logDir = join(root, ".dev-logs");
mkdirSync(logDir, { recursive: true });
writeFileSync(
  join(logDir, "README.txt"),
  `由 scripts/dev.mjs 生成，可安全删除。\nNode：${node.text}（${node.dir}）\n`,
);

// ── 4. 收尾机制：只结束本脚本启动的进程组 ────────────────────────────────
// 定义在启动之前，这样启动失败、就绪超时等任何失败路径都能走同一套收尾，
// 不会留下已启动的另一个服务成为孤儿进程。
const children = [];
const logs = new Map();
let closing = false;

const stopChild = (child) =>
  new Promise((resolve) => {
    if (child.exitCode !== null || child.signalCode !== null)
      return resolve();
    const signal = (name) => {
      try {
        // 子进程是独立进程组组长，收掉整组才能带上 npm/go run 派生的孙进程。
        process.kill(-child.pid, name);
      } catch {
        try {
          child.kill(name);
        } catch {
          /* 进程已不存在 */
        }
      }
    };
    const timer = setTimeout(() => signal("SIGKILL"), 5000);
    child.once("exit", () => {
      clearTimeout(timer);
      resolve();
    });
    signal("SIGTERM");
  });

const shutdown = async (code, reason) => {
  if (closing) return;
  closing = true;
  if (reason) console.log(`\n${reason}`);
  if (children.length > 0) console.log("正在停止预览服务…");
  await Promise.all(children.map(({ child }) => stopChild(child)));
  if (children.length > 0) console.log(`已停止。日志：${logDir}`);
  process.exit(code);
};

// ── 5. 启动子进程 ────────────────────────────────────────────────────────
const streamLogs = (child, name, color, logPath) => {
  const file = createWriteStream(logPath, { flags: "w" });
  logs.set(name, logPath);
  // 终端带颜色便于区分来源；写盘用纯文本，避免日志文件里混入 ANSI 序列。
  const colored = `\x1b[${color}m[${name}]\x1b[0m `;
  const plain = `[${name}] `;
  let carry = "";
  const write = (line) => {
    file.write(`${plain}${line}\n`);
    process.stdout.write(`${colored}${line}\n`);
  };
  const attach = (stream) => {
    stream.on("data", (chunk) => {
      const lines = (carry + chunk.toString()).split("\n");
      carry = lines.pop();
      for (const line of lines) write(line);
    });
    stream.on("end", () => {
      if (carry) write(carry);
      carry = "";
    });
  };
  attach(child.stdout);
  attach(child.stderr);
};

const start = (name, command, args, { cwd, env = {}, color }) => {
  const child = spawn(command, args, {
    cwd,
    env: { ...process.env, ...env },
    stdio: ["ignore", "pipe", "pipe"],
    detached: true, // 独立进程组，便于连同子进程一起收掉
  });
  // spawn 失败（如 go / npm 不在 PATH）也要走统一收尾，避免另一个服务残留。
  child.on("error", (error) =>
    shutdown(1, `\x1b[31m✗ ${name} 启动失败：${error.message}\x1b[0m`),
  );
  streamLogs(child, name, color, join(logDir, `${name}.log`));
  children.push({ name, child });
  return child;
};

const backend = start(
  "backend",
  "go",
  ["run", "./cmd/server", "--dev", "--port", String(BACKEND_PORT)],
  { cwd: join(root, "backend"), color: "36" },
);
const frontend = start("frontend", "npm", ["run", "dev"], {
  cwd: join(root, "frontend"),
  env: envWithNode(node.dir),
  color: "35",
});

// 启动失败时的收尾入口。此前这两处调用的 die() 从未被定义，抛出的
// ReferenceError 会让顶层 await 直接拒绝，shutdown() 永不执行 —— 已拉起的
// Vite（detached 进程组）继续占着端口，且看不到日志路径提示。
// shutdown 自带 closing 幂等保护，重复调用安全。
const die = (message) => shutdown(1, message);

// ── 5. 信号处理（收尾机制已在第 4 节定义） ───────────────────────────────
process.on("SIGINT", () => shutdown(0, "\n收到 Ctrl-C。"));
process.on("SIGTERM", () => shutdown(0, "\n收到终止信号。"));

// ── 6. 等待就绪 ──────────────────────────────────────────────────────────
const waitFor = async (label, url, child, timeoutMs) => {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (child.exitCode !== null || child.signalCode !== null) {
      // await + return: die() resolves only after shutdown has stopped the
      // siblings and called process.exit, so the poll loop must not continue
      // in the meantime.
      await die(
        `${label}进程已退出（退出码 ${child.exitCode}），未就绪。日志：${logs.get(label)}`,
      );
      return;
    }
    try {
      const response = await fetch(url, { signal: AbortSignal.timeout(2000) });
      if (response.ok) return;
    } catch {
      /* 还没起来，继续等 */
    }
    await sleep(400);
  }
  await die(`${label}在 ${timeoutMs / 1000}s 内未就绪：${url}`);
};

// go run 首次需要编译，给足时间。
await waitFor("backend", backendUrl, backend, 180000);
console.log("\x1b[32m✓\x1b[0m 后端就绪");
await waitFor("frontend", frontendUrl, frontend, 90000);
console.log("\x1b[32m✓\x1b[0m 前端就绪");

console.log(`
────────────────────────────────────────────────────────
  预览地址   \x1b[1mhttp://127.0.0.1:${FRONTEND_PORT}${BASE_PATH}/\x1b[0m
  后端接口   http://127.0.0.1:${BACKEND_PORT}${BASE_PATH}/svr/session
  开发数据   ${process.env.OPENSYNC_DATA_DIR || join(root, "backend", "data")}
  日志       ${logDir}
────────────────────────────────────────────────────────
  Ctrl-C 停止前后端。两个服务仅监听 127.0.0.1，请勿对外暴露。
`);

// ── 7. 任一服务退出即整体收尾 ────────────────────────────────────────────
for (const { name, child } of children)
  child.on("exit", (code, signal) => {
    if (closing) return;
    shutdown(
      code ?? 1,
      `\x1b[31m✗ ${name} 已退出（退出码 ${code}，信号 ${signal}），停止其余服务。\x1b[0m`,
    );
  });
