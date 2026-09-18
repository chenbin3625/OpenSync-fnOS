import { spawnSync } from "node:child_process";
import {
  cpSync,
  mkdirSync,
  readFileSync,
  writeFileSync,
  renameSync,
} from "node:fs";
import { fileURLToPath } from "node:url";
import { join } from "node:path";

const root = fileURLToPath(new URL("../", import.meta.url));
const arch = process.argv[2] || "amd64";
if (!["amd64", "arm64"].includes(arch))
  throw new Error("架构必须是 amd64 或 arm64");
const run = (command, args, cwd = root, env = {}) => {
  const result = spawnSync(command, args, {
    cwd,
    env: { ...process.env, ...env },
    stdio: "inherit",
  });
  if (result.error || result.status !== 0)
    throw result.error || new Error(`${command} 构建失败`);
};
run("npm", ["run", "build"], join(root, "frontend"));
const pack = join(root, "dist", `opensync-${arch}`);
mkdirSync(pack, { recursive: true });
cpSync(join(root, "fnos"), pack, { recursive: true });
cpSync(join(root, "LICENSE"), join(pack, "LICENSE"));
const manifest = readFileSync(join(pack, "manifest"), "utf8").replace(
  /^platform=.+$/m,
  `platform=${arch === "amd64" ? "x86" : "arm"}`,
);
writeFileSync(join(pack, "manifest"), manifest);
mkdirSync(join(pack, "app", "server"), { recursive: true });
run(
  "go",
  [
    "build",
    "-trimpath",
    "-ldflags=-s -w",
    "-o",
    join(pack, "app", "server", "opensync"),
    "./cmd/server",
  ],
  join(root, "backend"),
  { GOOS: "linux", GOARCH: arch, CGO_ENABLED: "0" },
);
run(
  process.env.FNPACK || join(root, ".tools", "fnpack"),
  ["build", "--directory", pack],
  join(root, "dist"),
);
renameSync(
  join(root, "dist", "opensync.fpk"),
  join(root, "dist", `opensync-${arch}.fpk`),
);
console.log(`已生成 ${arch} 安装包；发布前仍需在飞牛设备完成验收。`);
