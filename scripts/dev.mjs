#!/usr/bin/env node
// 一键启动本地预览：Go 后端（--dev，按显式配置监听可信局域网）+ Vite 前端开发服务。
// 用法：node scripts/dev.mjs  或  npm run dev
import { spawn } from "node:child_process";
import { createWriteStream, existsSync, mkdirSync, realpathSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
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

const BASE_PATH = "/app/opensync";
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

export const createDevSupervisor = ({
  spawnFn = spawn,
  fetchFn = fetch,
  exitFn = (code) => process.exit(code),
  killFn = (pid, signal) => process.kill(pid, signal),
  stdout = process.stdout,
  log = console.log,
  logDir = join(root, ".dev-logs"),
  stopGraceMs = 5000,
  pollIntervalMs = 400,
} = {}) => {
  const children = [];
  const logs = new Map();
  const spawnErrors = new WeakSet();
  let closing = false;
  let shutdownPromise = null;

  const stopChild = (child) =>
    new Promise((resolve) => {
      if (
        !child ||
        !child.pid ||
        child.exitCode !== null ||
        child.signalCode !== null ||
        spawnErrors.has(child)
      ) {
        return resolve();
      }
      let settled = false;
      let timer;
      const finish = () => {
        if (settled) return;
        settled = true;
        if (timer) clearTimeout(timer);
        child.removeListener("exit", finish);
        child.removeListener("error", finish);
        resolve();
      };
      const sendSignal = (name) => {
        if (!child.pid) return;
        try {
          // 子进程是独立进程组组长，收掉整组才能带上 npm/go run 派生的孙进程。
          killFn(-child.pid, name);
        } catch {
          try {
            child.kill(name);
          } catch {
            /* 进程已不存在 */
          }
        }
      };
      child.once("exit", finish);
      child.once("error", finish);
      if (
        child.exitCode !== null ||
        child.signalCode !== null ||
        spawnErrors.has(child)
      ) {
        return finish();
      }
      timer = setTimeout(() => sendSignal("SIGKILL"), stopGraceMs);
      sendSignal("SIGTERM");
    });

  const shutdown = (code, reason) => {
    if (shutdownPromise) return shutdownPromise;
    closing = true;
    shutdownPromise = (async () => {
      if (reason) log(`\n${reason}`);
      if (children.length > 0) log("正在停止预览服务…");
      await Promise.all(children.map(({ child }) => stopChild(child)));
      if (children.length > 0) log(`已停止。日志：${logDir}`);
      exitFn(code);
    })();
    return shutdownPromise;
  };

  const streamLogs = (child, name, color, logPath) => {
    const file = createWriteStream(logPath, { flags: "w" });
    file.on("error", () => {});
    logs.set(name, logPath);
    // 终端带颜色便于区分来源；写盘用纯文本，避免日志文件里混入 ANSI 序列。
    const colored = `\x1b[${color}m[${name}]\x1b[0m `;
    const plain = `[${name}] `;
    const write = (line) => {
      file.write(`${plain}${line}\n`);
      stdout.write(`${colored}${line}\n`);
    };
    let openStreams = 0;
    const closeStream = () => {
      openStreams -= 1;
      if (openStreams <= 0) file.end();
    };
    const attach = (stream) => {
      if (!stream) return;
      openStreams += 1;
      let carry = "";
      let closed = false;
      const flush = () => {
        if (carry) {
          write(carry);
          carry = "";
        }
      };
      const done = () => {
        flush();
        if (!closed) {
          closed = true;
          closeStream();
        }
      };
      stream.on("data", (chunk) => {
        const lines = (carry + chunk.toString()).split("\n");
        carry = lines.pop() ?? "";
        for (const line of lines) write(line);
      });
      stream.on("end", done);
      stream.on("close", done);
    };
    attach(child.stdout);
    attach(child.stderr);
    if (openStreams === 0) file.end();
  };

  const start = (name, command, args, { cwd, env = {}, color }) => {
    const child = spawnFn(command, args, {
      cwd,
      env: { ...process.env, ...env },
      stdio: ["ignore", "pipe", "pipe"],
      detached: true, // 独立进程组，便于连同子进程一起收掉
    });
    const logPath = join(logDir, `${name}.log`);
    logs.set(name, logPath);
    streamLogs(child, name, color, logPath);
    children.push({ name, child });
    // spawn 失败（如 go / npm 不在 PATH）也要走统一收尾，避免另一个服务残留。
    child.on("error", (error) => {
      spawnErrors.add(child);
      void shutdown(1, `\x1b[31m✗ ${name} 启动失败：${error.message}\x1b[0m`);
    });
    // 退出监听在拉起时立即注册：等待另一个服务就绪期间本服务挂掉，也要立刻整体
    // 收尾，而不是等两个都就绪后才发现、把已退出的服务报告为“就绪”。
    child.on("exit", (code, signal) => {
      if (closing) return;
      void shutdown(
        code || 1,
        `\x1b[31m✗ ${name} 已退出（退出码 ${code}，信号 ${signal}），停止其余服务。日志：${logs.get(name)}\x1b[0m`,
      );
    });
    return child;
  };

  const die = async (message) => {
    await shutdown(1, message);
    throw new Error(message);
  };

  const waitFor = async (label, url, child, timeoutMs) => {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
      if (closing) {
        await shutdownPromise;
        throw new Error(`${label} 启动已中止`);
      }
      if (
        child.exitCode !== null ||
        child.signalCode !== null ||
        spawnErrors.has(child)
      ) {
        await die(
          `${label}进程已退出（退出码 ${child.exitCode}），未就绪。日志：${logs.get(label)}`,
        );
      }
      try {
        const response = await fetchFn(url, {
          signal: AbortSignal.timeout(2000),
        });
        if (response.ok) return;
      } catch {
        /* 还没起来，继续等 */
      }
      await sleep(pollIntervalMs);
    }
    await die(`${label}在 ${timeoutMs / 1000}s 内未就绪：${url}`);
  };

  return {
    children,
    logs,
    stopChild,
    shutdown,
    streamLogs,
    start,
    die,
    waitFor,
    isClosing: () => closing,
  };
};

const runCli = async () => {
  // 端口来自 vite.config.ts：代理目标即后端端口，server.port 即前端端口。
  // 脚本不重复声明，改了配置这里自动跟随。
  const { frontendPort: FRONTEND_PORT, backendPort } = readFrontendConfig();
  if (!backendPort)
    throw new Error("vite.config.ts 未声明后端代理目标，无法确定后端端口");
  const BACKEND_PORT = backendPort;

  const backendUrl = `http://127.0.0.1:${BACKEND_PORT}${BASE_PATH}/svr/session`;
  const frontendUrl = `http://127.0.0.1:${FRONTEND_PORT}${BASE_PATH}/`;

  const argv = process.argv.slice(2);
  if (argv.includes("--help") || argv.includes("-h")) {
    console.log(`一键启动前后端预览。

  node scripts/dev.mjs [--no-install]

前端监听 0.0.0.0:${FRONTEND_PORT}${BASE_PATH}/  后端监听 0.0.0.0:${BACKEND_PORT}
端口取自 vite.config.ts；改端口请改配置，脚本会自动跟随。

  --no-install   缺少 frontend/node_modules 时直接报错，不自动执行 npm ci
`);
    process.exit(0);
  }
  const autoInstall = !argv.includes("--no-install");

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

  // ── 4. 收尾机制与启动子进程 ──────────────────────────────────────────────
  const supervisor = createDevSupervisor({ logDir });

  process.on("SIGINT", () => void supervisor.shutdown(0, "\n收到 Ctrl-C。"));
  process.on("SIGTERM", () => void supervisor.shutdown(0, "\n收到终止信号。"));

  const bundledDevRclone = join(root, ".tools", "rclone", "bin", "rclone");
  const backendEnv = { OPENSYNC_DEV_HOST: "0.0.0.0" };
  if (!process.env.OPENSYNC_RCLONE_BIN && existsSync(bundledDevRclone)) {
    backendEnv.OPENSYNC_RCLONE_BIN = bundledDevRclone;
  }
  const backend = supervisor.start(
    "backend",
    "go",
    ["run", "./cmd/server", "--dev", "--port", String(BACKEND_PORT)],
    {
      cwd: join(root, "backend"),
      env: backendEnv,
      color: "36",
    },
  );
  const frontend = supervisor.start("frontend", "npm", ["run", "dev"], {
    cwd: join(root, "frontend"),
    env: envWithNode(node.dir),
    color: "35",
  });

  // ── 5. 等待就绪 ──────────────────────────────────────────────────────────
  // go run 首次需要编译，给足时间。
  await supervisor.waitFor("backend", backendUrl, backend, 180000);
  console.log("\x1b[32m✓\x1b[0m 后端就绪");
  await supervisor.waitFor("frontend", frontendUrl, frontend, 90000);
  console.log("\x1b[32m✓\x1b[0m 前端就绪");

  console.log(`
────────────────────────────────────────────────────────
  预览地址   \x1b[1mhttp://<本机局域网地址>:${FRONTEND_PORT}${BASE_PATH}/\x1b[0m
  后端接口   监听 0.0.0.0:${BACKEND_PORT}${BASE_PATH}/svr/session
  开发数据   ${process.env.OPENSYNC_DATA_DIR || join(root, "backend", "data")}
  日志       ${logDir}
────────────────────────────────────────────────────────
  Ctrl-C 停止前后端。开发后端关闭 fnOS 鉴权，请仅在可信局域网使用。
`);
};

const isMain = (() => {
  if (!process.argv[1]) return false;
  try {
    return realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url));
  } catch {
    return false;
  }
})();

if (isMain) {
  await runCli();
}
