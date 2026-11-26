import { readFileSync } from "node:fs";
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
  creatableRemoteTypes,
  draftFromRemote,
  emptyDraft,
  remoteTypeGroups,
  remoteTypeLabels,
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

  it("网盘 OAuth/向导类型在完成图形化授权会话后无需手动粘贴 Token 即可通过校验", () => {
    for (const type of [
      "onedrive",
      "drive",
      "dropbox",
      "jottacloud",
      "googlecloudstorage",
    ] as const) {
      const missingAuth = { ...emptyDraft(type), name: `disk_${type}` };
      const errs = validateRemoteDraft(missingAuth, true);
      expect(Object.keys(errs).length).toBeGreaterThan(0);

      const withSession = {
        ...missingAuth,
        authSessionId: "auth-session-1",
        authConfiguredSecrets: ["token"],
      };
      expect(validateRemoteDraft(withSession, true)).toEqual({});
    }
  });

  it("远程存储配置表单不包含要求用户手动执行 rclone authorize 或 rclone config 命令的提示", () => {
    const src = readFileSync(
      new URL("../src/components/NativeStorageEditor.tsx", import.meta.url),
      "utf8",
    );
    expect(src).not.toContain("rclone authorize");
    expect(src).not.toContain("rclone config");
  });

  it("添加远程存储仅按分组暴露真实可写存储，不包含虚拟/只读/临时分享类型", () => {
    expect(remoteTypeGroups.map((g) => g.key)).toEqual([
      "protocols",
      "cloud_drives",
      "object_storage",
    ]);
    expect(new Set(creatableRemoteTypes).size).toBe(creatableRemoteTypes.length);
    expect(creatableRemoteTypes).toHaveLength(32);
    expect(creatableRemoteTypes).toContain("quark");
    expect(creatableRemoteTypes).toContain("pan123");
    expect(remoteTypeLabels.quark).toBe("夸克网盘");
    expect(remoteTypeLabels.pan123).toBe("123云盘");
    for (const typ of creatableRemoteTypes) {
      expect(remoteTypeLabels[typ]).toBeTruthy();
    }

    // Virtual wrappers, composites, read-only sources, and temporary file hosts
    // must stay out of the "添加远程存储" selector.
    for (const excluded of [
      "crypt",
      "chunker",
      "compress",
      "hasher",
      "alias",
      "archive",
      "union",
      "combine",
      "http",
      "fichier",
      "gofile",
      "pixeldrain",
    ]) {
      expect(creatableRemoteTypes).not.toContain(excluded);
    }
  });

  it("编辑已有或导入的加密/复合/只读配置时保持原类型兼容", () => {
    const legacyRemotes: Array<{
      name: string;
      type: string;
      options: Record<string, string>;
      configuredSecrets: string[];
    }> = [
      {
        name: "vault",
        type: "crypt",
        options: { remote: "archive:bucket/enc" },
        configuredSecrets: ["password"],
      },
      {
        name: "merged",
        type: "union",
        options: { upstreams: "s3a:bucket s3b:bucket" },
        configuredSecrets: [],
      },
      {
        name: "mirror",
        type: "http",
        options: { url: "https://mirrors.example.com/files/" },
        configuredSecrets: [],
      },
      {
        name: "chunked",
        type: "chunker",
        options: { remote: "archive:bucket" },
        configuredSecrets: [],
      },
    ];
    for (const item of legacyRemotes) {
      const draft = draftFromRemote({
        ...item,
        summary: "",
        usedByJobs: [],
        usedByCrypts: [],
      });
      expect(draft.editing).toBe(true);
      expect(draft.type).toBe(item.type);
      expect(validateRemoteDraft(draft, true)).toEqual({});
      expect(buildRemoteOptions(draft).type).toBe(item.type);
    }
  });

  it("夸克网盘支持扫码登录会话、手动 Cookie 与编辑保留原 Cookie，并校验并发和限流参数", () => {
    const emptyQuark = { ...emptyDraft("quark"), name: "我的夸克" };
    expect(validateRemoteDraft(emptyQuark, true)).toHaveProperty("cookie");

    // Completed QR login session passes without manually typing cookie.
    const qrLoggedIn = {
      ...emptyQuark,
      authSessionId: "qr-sess-1",
      authConfiguredSecrets: ["cookie"],
      options: { account_name: "夸克测试号" },
    };
    expect(validateRemoteDraft(qrLoggedIn, true)).toEqual({});
    expect(buildRemoteOptions(qrLoggedIn)).toEqual({
      type: "quark",
      account_name: "夸克测试号",
    });

    // Manual cookie with valid concurrency & TPS passes.
    const manualQuark = {
      ...emptyQuark,
      options: {
        cookie: "__pus=abc; __puus=def",
        root_folder_id: "0",
        upload_concurrency: "4",
        tps_limit: "3.5",
      },
    };
    expect(validateRemoteDraft(manualQuark, true)).toEqual({});
    const built = buildRemoteOptions(manualQuark);
    expect(built.type).toBe("quark");
    expect(built.cookie).toBe("__pus=abc; __puus=def");
    expect(built.upload_concurrency).toBe("4");

    // Out-of-range upload_concurrency and tps_limit fail validation.
    expect(
      validateRemoteDraft(
        {
          ...manualQuark,
          options: { ...manualQuark.options, upload_concurrency: "12" },
        },
        true,
      ),
    ).toHaveProperty("upload_concurrency");
    expect(
      validateRemoteDraft(
        {
          ...manualQuark,
          options: { ...manualQuark.options, tps_limit: "50" },
        },
        true,
      ),
    ).toHaveProperty("tps_limit");

    // Editing an existing Quark remote preserves saved cookie without re-entering.
    const existing = draftFromRemote({
      name: "我的夸克",
      type: "quark",
      summary: "夸克网盘 · 夸克测试号",
      options: { root_folder_id: "0", account_name: "夸克测试号" },
      configuredSecrets: ["cookie"],
      usedByJobs: [],
      usedByCrypts: [],
    });
    expect(existing.editing).toBe(true);
    expect(existing.type).toBe("quark");
    expect(validateRemoteDraft(existing, true)).toEqual({});
  });

  it("123云盘支持 OpenAPI 与个人盘 Web 双协议配置、编辑保留密钥及参数校验", () => {
    const emptyPan123 = { ...emptyDraft("pan123"), name: "我的123云盘" };
    const openErrs = validateRemoteDraft(emptyPan123, true);
    expect(openErrs).toHaveProperty("client_id");
    expect(openErrs).toHaveProperty("client_secret");

    // OpenAPI with Client ID + Secret passes and defaults protocol to open_api.
    const openValid = {
      ...emptyPan123,
      options: {
        client_id: "cid-1",
        client_secret: "csec-1",
        upload_concurrency: "4",
        tps_limit: "3",
        use_direct_link: "true",
        direct_link_key: "dl-key",
        direct_link_ttl: "30",
      },
    };
    expect(validateRemoteDraft(openValid, true)).toEqual({});
    const builtOpen = buildRemoteOptions(openValid);
    expect(builtOpen.type).toBe("pan123");
    expect(builtOpen.protocol).toBe("open_api");
    expect(builtOpen.client_id).toBe("cid-1");
    expect(builtOpen.client_secret).toBe("csec-1");

    // OpenAPI with static Access Token passes without Client ID/Secret.
    const openTokenOnly = {
      ...emptyPan123,
      options: { protocol: "open_api", access_token: "static-open-token" },
    };
    expect(validateRemoteDraft(openTokenOnly, true)).toEqual({});

    // Personal Web protocol requires web_user + web_password or access_token.
    const webEmpty = {
      ...emptyPan123,
      options: { protocol: "web" },
    };
    expect(validateRemoteDraft(webEmpty, true)).toHaveProperty("web_user");
    expect(validateRemoteDraft(webEmpty, true)).toHaveProperty("web_password");

    const webTokenOnly = {
      ...emptyPan123,
      options: { protocol: "web", access_token: "jwt-web-token" },
    };
    expect(validateRemoteDraft(webTokenOnly, true)).toEqual({});
    expect(buildRemoteOptions(webTokenOnly).protocol).toBe("web");

    // Invalid root_folder_id / upload_concurrency / direct_link_ttl rejected.
    expect(
      validateRemoteDraft(
        {
          ...openTokenOnly,
          options: { ...openTokenOnly.options, root_folder_id: "not-a-number" },
        },
        true,
      ),
    ).toHaveProperty("root_folder_id");
    expect(
      validateRemoteDraft(
        {
          ...openTokenOnly,
          options: { ...openTokenOnly.options, upload_concurrency: "10" },
        },
        true,
      ),
    ).toHaveProperty("upload_concurrency");

    // Editing an existing 123Pan remote preserves saved secrets without re-entering.
    const existing = draftFromRemote({
      name: "我的123云盘",
      type: "pan123",
      summary: "123云盘 (开放平台)",
      options: { protocol: "open_api", client_id: "cid-1", root_folder_id: "0" },
      configuredSecrets: ["client_secret", "access_token"],
      usedByJobs: [],
      usedByCrypts: [],
    });
    expect(existing.editing).toBe(true);
    expect(validateRemoteDraft(existing, true)).toEqual({});
  });
});
