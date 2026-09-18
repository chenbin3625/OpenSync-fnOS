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
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync, spawn } from "node:child_process";
const root = fileURLToPath(new URL("../", import.meta.url));
test(
  "native service lifecycle protects identity, unrelated processes and upgrade data",
  { timeout: 60000 },
  async () => {
    const binary = process.env.OPENSYNC_TEST_BINARY;
    assert.ok(
      binary && existsSync(binary),
      "build a local test binary and set OPENSYNC_TEST_BINARY",
    );
    const fixture = mkdtempSync(join(tmpdir(), "opensync-lifecycle-"));
    for (const name of ["target/server", "var", "etc", "tmp", "cmd"])
      mkdirSync(join(fixture, name), { recursive: true });
    copyFileSync(binary, join(fixture, "target/server/opensync"));
    for (const name of ["main", "upgrade_init"]) {
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
