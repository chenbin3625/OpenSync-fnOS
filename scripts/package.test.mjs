import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
const root = fileURLToPath(new URL("../", import.meta.url));
test("package declares rootless gateway and minimum API scopes", () => {
  assert.ok(existsSync(root + "fnos/manifest"), "native manifest must exist");
  const manifest = readFileSync(root + "fnos/manifest", "utf8");
  assert.match(manifest, /^platform=x86$/m);
  assert.match(manifest, /^micro_app=true$/m);
  assert.match(manifest, /^os_min_version=1.2.0401$/m);
  assert.doesNotMatch(manifest, /^service_port=/m);
  assert.equal(
    JSON.parse(readFileSync(root + "fnos/config/privilege")).defaults["run-as"],
    "package",
  );
  // 应用只调用统一网关注入的身份头，不再访问共享目录或文件 ACL，
  // 因此不申请任何开放 API 权限。
  assert.deepEqual(
    JSON.parse(readFileSync(root + "fnos/config/resource"))["api-scope"],
    [],
  );
  const entry = JSON.parse(readFileSync(root + "fnos/app/ui/config"))[".url"][
    "opensync.main"
  ];
  assert.equal(entry.gatewayPrefix, "/app/opensync");
  assert.equal(entry.gatewaySocket, "app.sock");
  assert.equal(entry.allUsers, false);
  assert.equal(entry.type, "iframe");
  assert.equal(entry.control.accessPerm, "readonly");
});
