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
  creatableRemoteTypes,
  domesticDriveOpenListHints,
  hotStorageCards,
} from "../src/components/NativeStorageEditor";
import { analyzeOAuthCallbackInput } from "../src/components/NativeStorageAuthWizard";
import { buildQuickCryptOptions } from "../src/lib/quickCrypt";
import { MockStore } from "../src/mocks/store";

describe("Module 2: Storage cards, OpenList discovery, OAuth callback & crypt recovery", () => {
  it("exposes two-level storage cards and domestic OpenList hints while preserving 32 creatable protocols", () => {
    expect(creatableRemoteTypes).toHaveLength(32);
    const titles = hotStorageCards.map((c) => c.title);
    expect(titles).toEqual(
      expect.arrayContaining([
        "夸克网盘",
        "123云盘",
        "OpenList / AList (WebDAV)",
        "标准 WebDAV",
        "阿里云 OSS / MinIO / S3",
        "SMB / SFTP",
        "OneDrive",
        "Google Drive",
      ]),
    );
    expect(domesticDriveOpenListHints.map((h) => h.title)).toEqual([
      "阿里云盘",
      "百度网盘",
      "115网盘",
      "天翼云盘",
    ]);
  });

  it("recognizes full OAuth callback URLs, query strings, and URL fragments", () => {
    expect(
      analyzeOAuthCallbackInput("http://localhost:53682/?code=abc&state=st1"),
    ).toMatchObject({ recognized: true, hasCode: true, kindLabel: "完整回调网址" });

    expect(
      analyzeOAuthCallbackInput("?code=bare_query&state=st2"),
    ).toMatchObject({ recognized: true, hasCode: true, kindLabel: "纯查询串 (?code=...)" });

    expect(
      analyzeOAuthCallbackInput("http://127.0.0.1:53682/#/?code=hash_code&state=st3"),
    ).toMatchObject({ recognized: true, hasCode: true, kindLabel: "回调 URL 片段" });

    expect(
      analyzeOAuthCallbackInput("#code=frag_only&state=st4"),
    ).toMatchObject({ recognized: true, hasCode: true, kindLabel: "回调 URL 片段" });

    expect(
      analyzeOAuthCallbackInput("?error=access_denied&error_description=User%20cancelled"),
    ).toMatchObject({ recognized: true, hasCode: false, errorParam: "access_denied" });

    expect(analyzeOAuthCallbackInput("random text without params")).toEqual({
      recognized: false,
      hasCode: false,
    });
  });

  it("tags quick-crypt options with managed_by_job & managed_job_label when requested", () => {
    const plain = buildQuickCryptOptions(
      { remoteName: "oss", subPath: "backup" },
      {
        name: "vault",
        subDir: "enc",
        password: "secret",
        confirmPassword: "secret",
        password2: "",
        filenameEncryption: "standard",
        filenameEncoding: "base32",
        directoryNameEncryption: "true",
        noDataEncryption: false,
      },
    );
    expect(plain.managed_by_job).toBeUndefined();

    const managed = buildQuickCryptOptions(
      { remoteName: "oss", subPath: "backup" },
      {
        name: "vault",
        subDir: "enc",
        password: "secret",
        confirmPassword: "secret",
        password2: "",
        filenameEncryption: "standard",
        filenameEncoding: "base32",
        directoryNameEncryption: "true",
        noDataEncryption: false,
      },
      { managedByJob: true, managedJobLabel: "相册加密任务" },
    );
    expect(managed.managed_by_job).toBe("true");
    expect(managed.managed_job_label).toBe("相册加密任务");
  });

  it("mockStore handles discoverOpenList SSRF check, OAuth fragment tolerance, metadata export strip & crypt recovery", () => {
    const store = new MockStore();
    expect(store.discoverOpenList({ endpoints: ["http://192.168.1.99:5244/dav"] })).toContain(
      "本机回环地址",
    );
    const disc = store.discoverOpenList({ ports: [5244] });
    expect(typeof disc).toBe("object");
    if (typeof disc !== "string") {
      expect(disc.found).toBe(true);
      expect(disc.suggestedConfig?.vendor).toBe("other");
      expect(disc.suggestedConfig?.compareMode).toBe(1);
      expect(disc.suggestedConfig?.url).toBe("http://127.0.0.1:5244/dav");
    }

    // OAuth fragment callback support in MockStore
    const authStep = store.startNativeStorageAuth({ type: "onedrive", options: {} });
    expect(typeof authStep).toBe("object");
    if (typeof authStep !== "string") {
      const nextStep = store.submitNativeStorageAuthCallback(
        authStep.sessionId,
        "#/?code=mock_code&state=",
      );
      expect(typeof nextStep).toBe("object");
    }

    // Save job-managed crypt remote -> strip metadata in exportNativeConfig -> verify recovery bundle
    expect(
      store.saveNativeRemote({
        name: "baseoss",
        options: {
          type: "s3",
          provider: "Alibaba",
          endpoint: "oss-cn-hangzhou.aliyuncs.com",
          access_key_id: "ak",
          secret_access_key: "sk",
        },
      }),
    ).toBeNull();
    expect(
      store.saveNativeRemote({
        name: "jobcrypt",
        options: {
          type: "crypt",
          remote: "baseoss:bucket/encrypted",
          password: "StrongPassword!123",
          password2: "SaltValue!456",
          managed_by_job: "true",
          managed_job_label: "文档保密归档",
        },
      }),
    ).toBeNull();

    const exported = store.exportNativeConfig(["jobcrypt"]);
    expect(typeof exported).toBe("object");
    if (typeof exported !== "string") {
      expect(exported.content).not.toContain("managed_by_job");
      expect(exported.content).not.toContain("managed_job_label");
    }

    const recOk = store.getCryptRecovery({
      name: "jobcrypt",
      verifyPassword: "StrongPassword!123",
      verifyPassword2: "SaltValue!456",
    });
    expect(typeof recOk).toBe("object");
    if (typeof recOk !== "string") {
      expect(recOk.passwordVerified).toBe(true);
      expect(recOk.password2Verified).toBe(true);
      expect(recOk.bundle.managedByJob).toBe(true);
      expect(recOk.bundle.managedJobLabel).toBe("文档保密归档");
      expect(recOk.recoveryText).not.toContain("StrongPassword!123");
    }
  });
});
