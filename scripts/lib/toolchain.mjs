// 预览与发布脚本共用的工具：命令执行、Node 运行时定位、端口占用探测。
import { spawnSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { connect } from "node:net";
import { delimiter, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

export const root = fileURLToPath(new URL("../../", import.meta.url));

/** README 声明的最低 Node 版本；Vite 同样要求 20.19+ / 22.12+。 */
export const MIN_NODE = [22, 12, 0];

/** 同步执行命令并把输出直接接到当前终端。 */
export const run = (
  command,
  args,
  { cwd = root, env = {}, stdio = "inherit" } = {},
) => {
  const result = spawnSync(command, args, {
    cwd,
    env: { ...process.env, ...env },
    stdio,
  });
  if (result.error) throw result.error;
  if (result.status !== 0)
    throw new Error(
      `${command} ${args.join(" ")} 失败（退出码 ${result.status}）`,
    );
  return result;
};

/** 同步执行命令并捕获输出，用于探测类调用（不抛错，由调用方判断）。 */
export const capture = (command, args, { cwd = root, env = {} } = {}) =>
  spawnSync(command, args, {
    cwd,
    env: { ...process.env, ...env },
    encoding: "utf8",
  });

const parseVersion = (text) => {
  const match = /v?(\d+)\.(\d+)\.(\d+)/.exec(text || "");
  return match ? [+match[1], +match[2], +match[3]] : null;
};

const satisfies = (version, minimum) => {
  for (let index = 0; index < minimum.length; index += 1) {
    if (version[index] !== minimum[index]) return version[index] > minimum[index];
  }
  return true;
};

/**
 * 选出可用的 Node 运行时目录。返回目录而非二进制路径，因为 npm 的 shebang
 * 是通过 PATH 查找 node 的：只调用 homebrew 的 node 而不前置它的目录，
 * npm 仍会拉起 PATH 上更靠前的旧版本。
 */
export const pickNodeDir = () => {
  const dirs = new Set();
  if (process.env.OPENSYNC_NODE_DIR) dirs.add(process.env.OPENSYNC_NODE_DIR);
  dirs.add(dirname(process.execPath));
  for (const dir of (process.env.PATH || "").split(delimiter))
    if (dir) dirs.add(dir);
  for (const dir of ["/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"])
    dirs.add(dir);

  const candidates = [];
  const seen = new Set();
  for (const dir of dirs) {
    if (seen.has(dir)) continue;
    seen.add(dir);
    const node = join(dir, "node");
    // 目录里必须同时有 npm，否则前置它并不能修好 npm 的运行时。
    if (!existsSync(node) || !existsSync(join(dir, "npm"))) continue;
    const version = parseVersion(capture(node, ["-v"]).stdout);
    if (!version) continue;
    candidates.push({ dir, version, text: `v${version.join(".")}` });
  }
  if (candidates.length === 0)
    throw new Error("找不到 node 运行时，请确认已安装 Node.js 22.12+");

  candidates.sort((a, b) => b.version[0] - a.version[0] || b.version[1] - a.version[1] || b.version[2] - a.version[2]);
  const picked = candidates.find((item) => satisfies(item.version, MIN_NODE)) || candidates[0];
  return { ...picked, satisfiesMinimum: satisfies(picked.version, MIN_NODE) };
};

/** 把选中的 Node 目录前置到 PATH，供子进程（npm / vite / go）使用。 */
export const envWithNode = (nodeDir) => ({
  PATH: `${nodeDir}${delimiter}${process.env.PATH || ""}`,
});

const probe = (port) =>
  new Promise((resolve) => {
    const socket = connect({ host: "127.0.0.1", port });
    const done = (value) => {
      socket.destroy();
      resolve(value);
    };
    socket.setTimeout(1000);
    socket.once("connect", () => done(true));
    socket.once("timeout", () => done(false));
    socket.once("error", () => done(false));
  });

/**
 * 探测端口占用。lsof 提供 PID 便于用户定位；lsof 不可用时退回 TCP 探测，
 * 此时只能判断"被占用"而拿不到 PID。
 */
export const portOccupancy = async (port) => {
  const result = capture("lsof", [
    "-nP",
    `-iTCP:${port}`,
    "-sTCP:LISTEN",
    "-t",
  ]);
  if (result.error) {
    const busy = await probe(port);
    return { checked: false, busy, pids: [] };
  }
  const pids = [
    ...new Set(
      (result.stdout || "")
        .split("\n")
        .map((line) => line.trim())
        .filter(Boolean),
    ),
  ];
  if (pids.length > 0) return { checked: true, busy: true, pids };
  return { checked: true, busy: await probe(port), pids: [] };
};

/** 把 PID 解析成可读的进程名，便于用户判断能不能杀。 */
export const describePid = (pid) => {
  const result = capture("ps", ["-p", pid, "-o", "comm="]);
  return (result.stdout || "").trim() || "未知进程";
};

/**
 * 从 vite.config.ts 读取开发端口与后端代理目标。
 * 必须读配置而不是各自硬编码：否则改了 vite.config.ts 之后，脚本会去等一个
 * 永远不会监听的端口，最后只报出一个难懂的等待超时。
 */
export const readFrontendConfig = () => {
  const file = join(root, "frontend", "vite.config.ts");
  if (!existsSync(file))
    throw new Error(`找不到 ${file}，无法确定开发端口`);
  const source = readFileSync(file, "utf8");
  const port = /server:\s*\{[\s\S]*?port:\s*(\d+)/.exec(source);
  const target = /target:\s*["']http:\/\/127\.0\.0\.1:(\d+)["']/.exec(source);
  if (!port) throw new Error("vite.config.ts 未声明 server.port");
  return {
    frontendPort: Number(port[1]),
    backendPort: target ? Number(target[1]) : null,
  };
};
