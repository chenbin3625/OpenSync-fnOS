import { test } from "node:test";
import assert from "node:assert/strict";
import {
  mkdtempSync,
  mkdirSync,
  copyFileSync,
  readFileSync,
  writeFileSync,
  existsSync,
  chmodSync,
  rmSync,
  symlinkSync,
  readdirSync,
  utimesSync,
  truncateSync,
  appendFileSync,
  statSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync, spawn } from "node:child_process";
const root = fileURLToPath(new URL("../", import.meta.url));

const requireBinary = () => {
  const binary = process.env.OPENSYNC_TEST_BINARY;
  assert.ok(
    binary && existsSync(binary),
    "build a local test binary and set OPENSYNC_TEST_BINARY",
  );
  return binary;
};

const setupLifecycleFixture = ({ appdestSymlink = false } = {}) => {
  const binary = requireBinary();
  const fixture = mkdtempSync(join(tmpdir(), "opensync-lifecycle-"));
  for (const name of ["var", "etc", "tmp", "cmd"])
    mkdirSync(join(fixture, name), { recursive: true });
  const realTarget = join(fixture, appdestSymlink ? "real-target" : "target");
  mkdirSync(join(realTarget, "server"), { recursive: true });
  copyFileSync(binary, join(realTarget, "server/opensync"));
  if (appdestSymlink) symlinkSync(realTarget, join(fixture, "target"));
  for (const name of ["main", "upgrade_init", "uninstall_init", "config_callback"]) {
    copyFileSync(join(root, "fnos/cmd", name), join(fixture, "cmd", name));
    chmodSync(join(fixture, "cmd", name), 0o755);
  }
  const env = {
    ...process.env,
    TRIM_APPNAME: "opensync",
    TRIM_APPDEST: join(fixture, "target"),
    TRIM_PKGVAR: join(fixture, "var"),
    TRIM_PKGETC: join(fixture, "etc"),
    TRIM_PKGTMP: join(fixture, "tmp"),
    TRIM_TEMP_LOGFILE: join(fixture, "tmp/error"),
    TRIM_API_TOKEN: "fixture-not-a-real-platform-token",
  };
  const run = (operation, overrides = {}) =>
    spawnSync("bash", [join(fixture, "cmd/main"), operation], {
      env: { ...env, ...overrides },
      encoding: "utf8",
      timeout: 50000,
    });
  return { fixture, env, run };
};

test(
  "native service lifecycle protects identity, unrelated processes and upgrade data",
  { timeout: 60000 },
  async () => {
    const { fixture, env, run } = setupLifecycleFixture();
    try {
      assert.equal(run("status").status, 3);
      // 应用只读统一网关注入的身份头，不再调用飞牛开放 API，因此启动不应依赖
      // TRIM_API_TOKEN；缺少它也必须能起来（api-scope 为空时平台可能不注入）。
      const start = run("start", { TRIM_API_TOKEN: "" });
      assert.equal(start.status, 0, start.stderr);
      const first = readFileSync(join(fixture, "var/app.pid"), "utf8");
      assert.equal(run("start").status, 0);
      assert.equal(readFileSync(join(fixture, "var/app.pid"), "utf8"), first);
      assert.equal(run("status").status, 0);
      const fetch = (headers = []) =>
        spawnSync(
          "curl",
          [
            "-s",
            "--unix-socket",
            join(fixture, "target/app.sock"),
            ...headers,
            "http://localhost/app/opensync/svr/session",
          ],
          { encoding: "utf8" },
        );
      assert.equal(JSON.parse(fetch().stdout).code, 401);
      assert.equal(
        JSON.parse(
          fetch(["-H", "X-Trim-Userid: 1000", "-H", "X-Trim-Isadmin: true"])
            .stdout,
        ).data.development,
        false,
      );
      assert.equal(run("stop").status, 0);
      assert.equal(run("stop").status, 0);
      assert.equal(run("status").status, 3);
      assert.ok(existsSync(join(fixture, "var/openSync.db")));
      assert.ok(existsSync(join(fixture, "var/secret.key")));
      const unrelated = spawn("sleep", ["30"]);
      try {
        writeFileSync(join(fixture, "var/app.pid"), String(unrelated.pid));
        assert.equal(run("status").status, 3);
        assert.equal(run("stop").status, 0);
        assert.equal(unrelated.exitCode, null);
      } finally {
        unrelated.kill();
      }
      const upgrade = spawnSync("bash", [join(fixture, "cmd/upgrade_init")], {
        env,
        encoding: "utf8",
      });
      assert.equal(upgrade.status, 0, upgrade.stderr);
      const backup = readFileSync(
        join(fixture, "var/last-upgrade-backup"),
        "utf8",
      ).trim();
      assert.deepEqual(
        readFileSync(join(backup, "secret.key")),
        readFileSync(join(fixture, "var/secret.key")),
      );
      assert.deepEqual(
        readFileSync(join(backup, "openSync.db")),
        readFileSync(join(fixture, "var/openSync.db")),
      );
    } finally {
      run("stop");
      rmSync(fixture, { recursive: true });
    }
  },
);

test("native service lifecycle accepts fnOS symlinked target path", {
  timeout: 60000,
}, () => {
  const { fixture, run } = setupLifecycleFixture({ appdestSymlink: true });
  try {
    const start = run("start");
    assert.equal(start.status, 0, start.stderr);
    assert.equal(run("status").status, 0);
  } finally {
    run("stop");
    rmSync(fixture, { recursive: true });
  }
});

test("main compares canonical binary path for Linux proc identity checks", () => {
  const main = readFileSync(join(root, "fnos/cmd/main"), "utf8");
  assert.match(main, /BIN_REAL=/);
  assert.match(main, /readlink -f "\$BIN"/);
  assert.match(main, /\$executable" = "\$BIN_REAL"/);
});

test("uninstall_init stops a running native service before files are removed", {
  timeout: 60000,
}, () => {
  const { fixture, env, run } = setupLifecycleFixture();
  try {
    assert.equal(run("start").status, 0);
    const uninstall = spawnSync("bash", [join(fixture, "cmd/uninstall_init")], {
      env,
      encoding: "utf8",
      timeout: 50000,
    });
    assert.equal(uninstall.status, 0, uninstall.stderr);
    assert.equal(run("status").status, 3);
  } finally {
    run("stop");
    rmSync(fixture, { recursive: true });
  }
});

test("start ignores stale startup lock when the service is not running", {
  timeout: 60000,
}, () => {
  const { fixture, run } = setupLifecycleFixture();
  mkdirSync(join(fixture, "tmp/start.lock"));
  try {
    const start = run("start");
    assert.equal(start.status, 0, start.stderr);
    assert.equal(run("status").status, 0);
  } finally {
    run("stop");
    rmSync(fixture, { recursive: true });
  }
});

test("start treats a lock held by an unrelated live process as stale", {
  timeout: 60000,
}, () => {
  const { fixture, run } = setupLifecycleFixture();
  // PID 被无关的长驻进程复用：kill -0 成功，但它并不是本应用的 main start。
  const unrelated = spawn("sleep", ["30"]);
  mkdirSync(join(fixture, "tmp/start.lock"));
  writeFileSync(join(fixture, "tmp/start.lock/pid"), `${unrelated.pid}\n`);
  try {
    const start = run("start");
    assert.equal(start.status, 0, start.stderr);
    assert.equal(run("status").status, 0);
    assert.equal(unrelated.exitCode, null);
  } finally {
    unrelated.kill();
    run("stop");
    rmSync(fixture, { recursive: true });
  }
});

test("start still refuses while the lock holder is a live main start", {
  timeout: 60000,
}, () => {
  const { fixture, run } = setupLifecycleFixture();
  // 一个 argv 为 `… <fixture>/cmd/main start` 的长驻进程，代表仍在进行中的启动。
  const holder = spawn(
    "bash",
    ["-c", "sleep 30; true", join(fixture, "cmd/main"), "start"],
    { detached: true, stdio: "ignore" },
  );
  const deadline = Date.now() + 5000;
  while (
    !/ start\s*$/.test(
      spawnSync("ps", ["-p", String(holder.pid), "-o", "command="], {
        encoding: "utf8",
      }).stdout || "",
    ) &&
    Date.now() < deadline
  );
  mkdirSync(join(fixture, "tmp/start.lock"));
  writeFileSync(join(fixture, "tmp/start.lock/pid"), `${holder.pid}\n`);
  try {
    const start = run("start");
    assert.equal(start.status, 1);
    assert.match(start.stderr, /应用正在启动/);
    assert.equal(
      readFileSync(join(fixture, "tmp/start.lock/pid"), "utf8"),
      `${holder.pid}\n`,
    );
    assert.equal(run("status").status, 3);
  } finally {
    try {
      process.kill(-holder.pid, "SIGKILL");
    } catch {}
    run("stop");
    rmSync(fixture, { recursive: true });
  }
});

test("start rotates an oversized log and keeps at most five old logs", {
  timeout: 60000,
}, () => {
  const { fixture, run } = setupLifecycleFixture();
  const log = join(fixture, "var/app.log");
  for (let index = 1; index <= 5; index += 1)
    writeFileSync(`${log}.${index}`, `old-${index}\n`);
  writeFileSync(log, "current\n");
  truncateSync(log, 10 * 1024 * 1024);
  try {
    const start = run("start");
    assert.equal(start.status, 0, start.stderr);
    assert.equal(readFileSync(`${log}.1`).subarray(0, 8).toString(), "current\n");
    for (let index = 2; index <= 5; index += 1)
      assert.equal(readFileSync(`${log}.${index}`, "utf8"), `old-${index - 1}\n`);
    assert.ok(!existsSync(`${log}.6`));
    assert.ok(readFileSync(log).length < 1024 * 1024);
    // 未超过阈值时不轮转。
    assert.equal(run("stop").status, 0);
    assert.equal(run("start").status, 0);
    assert.equal(readFileSync(`${log}.2`, "utf8"), "old-1\n");
  } finally {
    run("stop");
    rmSync(fixture, { recursive: true });
  }
});

test("rotates app.log while the service is still running", {
  timeout: 60000,
}, async () => {
  const { fixture, run } = setupLifecycleFixture();
  const log = join(fixture, "var/app.log");
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  try {
    // start 的启动期轮转只在启动时判断大小；运行期由看护进程按同一阈值
    // copy-truncate。测试把检查间隔缩到 1s，服务已持有 app.log 的 fd，
    // 用 >> 追加模拟持续写入。
    const start = run("start", { OPENSYNC_LOG_CHECK_INTERVAL: "1" });
    assert.equal(start.status, 0, start.stderr);
    appendFileSync(log, "x".repeat(10 * 1024 * 1024 + 1024));
    const deadline = Date.now() + 20000;
    while (Date.now() < deadline) {
      if (existsSync(`${log}.1`) && statSync(log).size < 1024 * 1024) break;
      await sleep(200);
    }
    assert.ok(existsSync(`${log}.1`), "运行期间未生成 app.log.1");
    assert.ok(statSync(`${log}.1`).size >= 10 * 1024 * 1024);
    assert.ok(
      statSync(log).size < 1024 * 1024,
      "轮转后 app.log 未被截断（服务会继续写进旧 inode）",
    );
    // 看护进程不能持有生命周期锁：start 之后 stop 必须能立即拿到锁。
    assert.equal(run("stop").status, 0);
    assert.equal(run("status").status, 3);
  } finally {
    run("stop");
    rmSync(fixture, { recursive: true });
  }
});

test("upgrade_init prunes old backups and never touches unrelated entries", {
  timeout: 60000,
}, () => {
  const { fixture, env } = setupLifecycleFixture();
  const backups = join(fixture, "var/backups");
  mkdirSync(backups, { recursive: true });
  writeFileSync(join(fixture, "var/openSync.db"), "db");
  writeFileSync(join(fixture, "var/secret.key"), "key");
  const now = Date.now() / 1000;
  const old = ["upgrade.AAAAAAA1", "upgrade.AAAAAAA2", "upgrade.AAAAAAA3", "upgrade.AAAAAAA4"];
  old.forEach((name, index) => {
    mkdirSync(join(backups, name));
    writeFileSync(join(backups, name, "openSync.db"), name);
    const time = now - 1000 * (old.length - index);
    utimesSync(join(backups, name), time, time);
  });
  // last-upgrade-backup 记录的那份即使最旧也要保留。
  writeFileSync(
    join(fixture, "var/last-upgrade-backup"),
    `${join(backups, "upgrade.AAAAAAA1")}\n`,
  );
  // 名字不符合 mktemp 模式的条目必须原样保留。
  mkdirSync(join(backups, "upgrade.keep-me"));
  writeFileSync(join(backups, "notes.txt"), "keep");
  try {
    const upgrade = spawnSync("bash", [join(fixture, "cmd/upgrade_init")], {
      env,
      encoding: "utf8",
    });
    assert.equal(upgrade.status, 0, upgrade.stderr);
    const current = readFileSync(join(fixture, "var/last-upgrade-backup"), "utf8").trim();
    const remaining = readdirSync(backups).sort();
    assert.deepEqual(
      remaining,
      [
        "notes.txt",
        "upgrade.AAAAAAA1",
        "upgrade.AAAAAAA3",
        "upgrade.AAAAAAA4",
        "upgrade.keep-me",
        current.split("/").pop(),
      ].sort(),
    );
    assert.equal(readFileSync(join(current, "openSync.db"), "utf8"), "db");
  } finally {
    rmSync(fixture, { recursive: true });
  }
});

test("upgrade_init removes a partial backup and fails when a copy fails", {
  timeout: 60000,
}, () => {
  const { fixture, env } = setupLifecycleFixture();
  writeFileSync(join(fixture, "var/openSync.db"), "db");
  writeFileSync(join(fixture, "var/secret.key"), "key");
  writeFileSync(join(fixture, "var/last-upgrade-backup"), "/previous/backup\n");
  // 无读权限的文件让 cp 失败，模拟磁盘写满等复制中途出错。
  chmodSync(join(fixture, "var/secret.key"), 0o000);
  try {
    const upgrade = spawnSync("bash", [join(fixture, "cmd/upgrade_init")], {
      env,
      encoding: "utf8",
    });
    if (process.getuid?.() === 0) return; // root 可忽略权限，无法构造失败
    assert.notEqual(upgrade.status, 0);
    assert.match(upgrade.stderr, /升级备份失败/);
    const backups = join(fixture, "var/backups");
    assert.deepEqual(readdirSync(backups), []);
    assert.equal(
      readFileSync(join(fixture, "var/last-upgrade-backup"), "utf8"),
      "/previous/backup\n",
    );
  } finally {
    chmodSync(join(fixture, "var/secret.key"), 0o600);
    rmSync(fixture, { recursive: true });
  }
});

test("upgrade_init aborts before copying when free space is insufficient", {
  timeout: 60000,
}, () => {
  const { fixture, env } = setupLifecycleFixture();
  writeFileSync(join(fixture, "var/openSync.db"), "db");
  // 用假的 df 报告仅剩 1 KiB，验证复制前的空间检查。
  mkdirSync(join(fixture, "bin"));
  writeFileSync(
    join(fixture, "bin/df"),
    "#!/bin/sh\necho 'Filesystem 1024-blocks Used Available Capacity Mounted on'\necho '/dev/fake 2048 2047 1 100% /'\n",
  );
  chmodSync(join(fixture, "bin/df"), 0o755);
  try {
    const upgrade = spawnSync("bash", [join(fixture, "cmd/upgrade_init")], {
      env: { ...env, PATH: `${join(fixture, "bin")}:${process.env.PATH}` },
      encoding: "utf8",
    });
    assert.notEqual(upgrade.status, 0);
    assert.match(upgrade.stderr, /磁盘空间不足/);
    assert.match(readFileSync(join(fixture, "tmp/error"), "utf8"), /磁盘空间不足/);
    assert.deepEqual(readdirSync(join(fixture, "var/backups")), []);
    assert.ok(!existsSync(join(fixture, "var/last-upgrade-backup")));
  } finally {
    rmSync(fixture, { recursive: true });
  }
});

test("start reports failure when initialisation fails after binding", {
  timeout: 60000,
}, () => {
  const { fixture, run } = setupLifecycleFixture();
  try {
    // 损坏的数据库让 InitSQL 在绑定 Socket 之后失败；启动脚本以 app.sock 出现
    // 判定成功，因此 Socket 必须等初始化完成才发布，否则会把已退出的服务报告为已启动。
    writeFileSync(join(fixture, "var/openSync.db"), "not a sqlite database".repeat(200));
    const start = run("start");
    assert.notEqual(start.status, 0, "start succeeded with a corrupt database");
    assert.equal(run("status").status, 3);
    assert.ok(!existsSync(join(fixture, "target/app.sock")));
  } finally {
    run("stop");
    rmSync(fixture, { recursive: true });
  }
});

test("start refuses to delete a live renamed Unix socket (app.sock)", {
  timeout: 60000,
}, async () => {
  const { fixture, run } = setupLifecycleFixture();
  // 模拟 Go 后端先绑定 .app.sock.<pid> 再 rename 成 app.sock：
  // /proc/net/unix 里路径仍是临时名，fuser/ss -lx 均匹配不到 app.sock。
  const listener = spawn(
    "python3",
    [
      "-c",
      `
import os, signal, socket, sys, time
tmp = sys.argv[1]
final = sys.argv[2]
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.bind(tmp)
s.listen(5)
os.rename(tmp, final)
print("READY", flush=True)
signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
while True:
    try:
        conn, _ = s.accept()
        conn.close()
    except OSError:
        break
`,
      join(fixture, "target/.app.sock.99999"),
      join(fixture, "target/app.sock"),
    ],
    { stdio: ["ignore", "pipe", "pipe"] },
  );
  try {
    await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error("listener did not become ready")), 5000);
      listener.stdout.on("data", (chunk) => {
        if (chunk.toString().includes("READY")) {
          clearTimeout(timer);
          resolve();
        }
      });
      listener.on("exit", (code) => {
        clearTimeout(timer);
        reject(new Error(`listener exited early with ${code}`));
      });
    });

    const start = run("start");
    assert.notEqual(start.status, 0, "start must fail when renamed app.sock is live");
    assert.match(start.stderr, /Socket 仍被进程占用/);
    assert.ok(existsSync(join(fixture, "target/app.sock")), "live app.sock must not be deleted");

    // 确认 socket 仍可连接（未被破坏）。
    const probe = spawnSync(
      "python3",
      [
        "-c",
        "import socket,sys; s=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM); s.connect(sys.argv[1]); s.close()",
        join(fixture, "target/app.sock"),
      ],
      { encoding: "utf8" },
    );
    assert.equal(probe.status, 0, probe.stderr);
  } finally {
    listener.kill("SIGTERM");
    rmSync(fixture, { recursive: true, force: true });
  }
});

test("start cleans up child process when spawned process fails before execve", {
  timeout: 60000,
}, () => {
  const { fixture, env } = setupLifecycleFixture();
  // 用一个会 sleep 但从不 exec 成 $BIN 的脚本代替二进制，验证超时未 exec 时不会留下孤儿进程。
  const marker = `opensync-orphan-guard-${Date.now()}`;
  const fakeBin = join(fixture, "target/server/opensync");
  writeFileSync(fakeBin, `#!/bin/sh\nexec sleep 30 # ${marker}\n`);
  chmodSync(fakeBin, 0o755);
  try {
    const start = spawnSync("bash", [join(fixture, "cmd/main"), "start"], {
      env,
      encoding: "utf8",
      timeout: 20000,
    });
    assert.notEqual(start.status, 0);
    assert.match(start.stderr, /服务启动失败/);
    const leftover = spawnSync("pgrep", ["-f", marker], { encoding: "utf8" });
    assert.notEqual(leftover.status, 0, `orphan process remained: ${leftover.stdout}`);
  } finally {
    spawnSync("pkill", ["-f", marker]);
    rmSync(fixture, { recursive: true, force: true });
  }
});

test("stop terminates log-rotator and its child sleep without leaking orphans", {
  timeout: 60000,
}, () => {
  const { fixture, run } = setupLifecycleFixture();
  try {
    const interval = "287";
    const start = run("start", { OPENSYNC_LOG_CHECK_INTERVAL: interval });
    assert.equal(start.status, 0, start.stderr);
    const rotatorPidFile = join(fixture, "var/log-rotator.pid");
    assert.ok(existsSync(rotatorPidFile));
    const [rotatorPid] = readFileSync(rotatorPidFile, "utf8").trim().split(/\s+/);
    assert.ok(rotatorPid);

    assert.equal(run("stop").status, 0);
    assert.ok(!existsSync(rotatorPidFile));

    // rotator 及其派生 sleep 都必须已结束。
    const checkRotator = spawnSync("ps", ["-p", rotatorPid], { encoding: "utf8" });
    assert.notEqual(checkRotator.status, 0, "log-rotator process still running");
    const checkSleep = spawnSync(
      "pgrep",
      ["-f", `sleep ${interval}`],
      { encoding: "utf8" },
    );
    assert.notEqual(checkSleep.status, 0, `orphan sleep remained: ${checkSleep.stdout}`);
  } finally {
    run("stop");
    rmSync(fixture, { recursive: true, force: true });
  }
});

test("start acquires lifecycle lock before touching log-rotator or upgrade-pending", {
  timeout: 60000,
}, async () => {
  const { fixture, run } = setupLifecycleFixture();
  const pendingFile = join(fixture, "var/upgrade-pending");
  try {
    assert.equal(run("start").status, 0);
    writeFileSync(pendingFile, "/some/backup\n");
    const rotatorBefore = readFileSync(join(fixture, "var/log-rotator.pid"), "utf8");

    // 模拟另一个正在持锁的 lifecycle 操作（例如 stop/upgrade）。
    const holder = spawn(
      "flock",
      ["-x", join(fixture, "tmp/lifecycle.lock"), "sleep", "10"],
      { detached: true, stdio: "ignore" },
    );
    await new Promise((r) => setTimeout(r, 200));
    try {
      const start = run("start");
      assert.notEqual(start.status, 0);
      assert.match(start.stderr, /应用正在启动/);
      assert.ok(existsSync(pendingFile), "upgrade-pending must not be cleared without lock");
      assert.equal(
        readFileSync(join(fixture, "var/log-rotator.pid"), "utf8"),
        rotatorBefore,
        "log-rotator must not be restarted without lock",
      );
    } finally {
      try {
        process.kill(-holder.pid, "SIGKILL");
      } catch {}
    }
  } finally {
    run("stop");
    rmSync(fixture, { recursive: true, force: true });
  }
});

test("upgrade_init excludes log-rotator.pid and blocks concurrent start during backup", {
  timeout: 60000,
}, () => {
  const { fixture, env, run } = setupLifecycleFixture();
  try {
    assert.equal(run("start").status, 0);
    writeFileSync(join(fixture, "var/log-rotator.pid"), "999999 1\n");
    const upgrade = spawnSync("bash", [join(fixture, "cmd/upgrade_init")], {
      env,
      encoding: "utf8",
    });
    assert.equal(upgrade.status, 0, upgrade.stderr);
    const backup = readFileSync(join(fixture, "var/last-upgrade-backup"), "utf8").trim();
    assert.ok(!existsSync(join(backup, "log-rotator.pid")), "log-rotator.pid must be excluded");
  } finally {
    run("stop");
    rmSync(fixture, { recursive: true, force: true });
  }
});

test("fail still writes error to stderr when TRIM_TEMP_LOGFILE is unwritable", () => {
  const { fixture, env } = setupLifecycleFixture();
  try {
    const res = spawnSync("bash", [join(fixture, "cmd/main"), "invalid-op"], {
      env: { ...env, TRIM_TEMP_LOGFILE: join(fixture, "no-such-dir/err.log") },
      encoding: "utf8",
    });
    assert.equal(res.status, 1);
    assert.match(res.stderr, /不支持的运行操作/);
  } finally {
    rmSync(fixture, { recursive: true, force: true });
  }
});

test("rotates app.log while running without following a symlink at app.log.1", {
  timeout: 60000,
}, async () => {
  const { fixture, run } = setupLifecycleFixture();
  const log = join(fixture, "var/app.log");
  const victim = join(fixture, "var/victim.txt");
  writeFileSync(victim, "untouched");
  symlinkSync(victim, `${log}.1`);
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  try {
    const start = run("start", { OPENSYNC_LOG_CHECK_INTERVAL: "1" });
    assert.equal(start.status, 0, start.stderr);
    appendFileSync(log, "y".repeat(10 * 1024 * 1024 + 1024));
    const deadline = Date.now() + 20000;
    while (Date.now() < deadline) {
      if (statSync(log).size < 1024 * 1024) break;
      await sleep(200);
    }
    assert.equal(readFileSync(victim, "utf8"), "untouched");
    assert.ok(statSync(log).size < 1024 * 1024);
  } finally {
    run("stop");
    rmSync(fixture, { recursive: true, force: true });
  }
});
