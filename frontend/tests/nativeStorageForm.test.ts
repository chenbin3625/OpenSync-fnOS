import { describe, expect, it, vi } from "vitest";

vi.mock("@douyinfe/semi-icons", () => ({}));
vi.mock("@douyinfe/semi-ui/lib/es/button", () => ({ default: () => null }));
vi.mock("@douyinfe/semi-ui/lib/es/checkbox", () => ({ default: () => null }));
vi.mock("@douyinfe/semi-ui/lib/es/input", () => ({ default: () => null }));
vi.mock("@douyinfe/semi-ui/lib/es/modal", () => ({ default: () => null }));
vi.mock("@douyinfe/semi-ui/lib/es/select", () => ({ default: () => null }));
vi.mock("@douyinfe/semi-ui/lib/es/spin", () => ({ default: () => null }));
vi.mock("@douyinfe/semi-ui/lib/es/tag", () => ({ default: () => null }));
vi.mock("@douyinfe/semi-ui/lib/es/input/textarea", () => ({ default: () => null }));
vi.mock("@douyinfe/semi-ui/lib/es/toast", () => ({ default: {} }));
vi.mock("../src/components/common", () => ({}));
import {
  buildRemoteOptions,
  changeStorageAuthMode,
  draftFromRemote,
  emptyDraft,
  validateRemoteDraft,
} from "../src/components/NativeStorageEditor";

describe("原生存储认证表单", () => {
  for (const type of ["s3", "qingstor"] as const) {
    it(`${type} 支持运行时和匿名认证，并在切换时清除静态凭据`, () => {
      const existing = draftFromRemote({
        name: "archive",
        type,
        summary: "",
        options: { access_key_id: "saved-access", env_auth: "false" },
        configuredSecrets: ["secret_access_key", "session_token"],
        usedByJobs: [],
        usedByCrypts: [],
      });
      expect(existing.authMode).toBe("keys");
      expect(validateRemoteDraft(existing, true)).toEqual({});
      for (const mode of ["runtime", "anonymous"] as const) {
        const changed = changeStorageAuthMode(existing, mode);
        expect(validateRemoteDraft(changed, true)).toEqual({});
        const payload = buildRemoteOptions(changed);
        expect(payload.env_auth).toBe(mode === "runtime" ? "true" : "false");
        expect(payload.access_key_id).toBeUndefined();
        expect(payload.secret_access_key).toBeUndefined();
        expect(payload.session_token).toBeUndefined();
        expect(changed.clearSecrets).toEqual(expect.arrayContaining(["secret_access_key", "session_token"]));
        const edited = draftFromRemote({
          name: "archive", type, summary: "", options: payload,
          configuredSecrets: [], usedByJobs: [], usedByCrypts: [],
        });
        expect(edited.authMode).toBe(mode);
        expect(buildRemoteOptions(edited).env_auth).toBe(payload.env_auth);
      }
    });

    it(`${type} 默认要求完整密钥，认证模式能在未填密钥时保留`, () => {
      const draft = { ...emptyDraft(type), name: "new-storage" };
      expect(validateRemoteDraft(draft, true)).toHaveProperty("access_key_id");
      const anonymous = changeStorageAuthMode(draft, "anonymous");
      expect(validateRemoteDraft(anonymous, true)).toEqual({});
      const keys = changeStorageAuthMode(anonymous, "keys");
      expect(keys.authMode).toBe("keys");
      expect(validateRemoteDraft(keys, true)).toHaveProperty("secret_access_key");
    });
  }

  it("编辑 OCI 存储保留所选认证 provider", () => {
    const draft = draftFromRemote({
      name: "oci", type: "oracleobjectstorage", summary: "",
      options: { provider: "no_auth", namespace: "ns", region: "us-phoenix-1" },
      configuredSecrets: [], usedByJobs: [], usedByCrypts: [],
    });
    expect(validateRemoteDraft(draft, true)).toEqual({});
    expect(buildRemoteOptions(draft).provider).toBe("no_auth");
  });

  it("S3 从 keys 切到 runtime 再切回 keys 时撤销 clearSecrets 标记", () => {
    const existing = draftFromRemote({
      name: "oss",
      type: "s3",
      summary: "",
      options: { access_key_id: "ak", env_auth: "false" },
      configuredSecrets: ["secret_access_key", "session_token"],
      usedByJobs: [],
      usedByCrypts: [],
    });
    const toRuntime = changeStorageAuthMode(existing, "runtime");
    expect(toRuntime.clearSecrets).toEqual(
      expect.arrayContaining(["secret_access_key", "session_token"]),
    );
    const backToKeys = changeStorageAuthMode(toRuntime, "keys");
    expect(backToKeys.authMode).toBe("keys");
    expect(backToKeys.clearSecrets).not.toContain("secret_access_key");
    expect(backToKeys.clearSecrets).not.toContain("session_token");
  });

  it("Swift 使用账号或应用凭据时要求提供有效 auth 地址，Storage URL + Token 模式除外", () => {
    const missingAuth = {
      ...emptyDraft("swift"),
      name: "sw1",
      options: { user: "alice", key: "secret" },
    };
    expect(validateRemoteDraft(missingAuth, true)).toHaveProperty("auth");

    const validAuth = {
      ...emptyDraft("swift"),
      name: "sw2",
      options: { auth: "https://identity.example.com/v3", user: "alice", key: "secret" },
    };
    expect(validateRemoteDraft(validAuth, true)).toEqual({});

    const storageTokenMode = {
      ...emptyDraft("swift"),
      name: "sw3",
      options: { storage_url: "https://swift.example.com/v1/acc", auth_token: "tok" },
    };
    expect(validateRemoteDraft(storageTokenMode, true)).toEqual({});
  });
});
