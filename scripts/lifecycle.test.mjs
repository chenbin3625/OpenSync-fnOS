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
  for (const name of ["main", "upgrade_init", "uninstall_init"]) {
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
