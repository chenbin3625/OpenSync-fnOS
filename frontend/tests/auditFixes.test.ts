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
  appendCombineUpstream,
  buildRemoteOptions,
  draftFromRemote,
  validateRemoteName,
} from "../src/components/NativeStorageEditor";
import {
  buildJobPayload,
  defaultJobForm,
  findStandardWebDAVRemotesForPaths,
  isPathDisabledInPicker,
  resolveAutoCompareModeForDst,
  validateJobFormFields,
  validateManualSyncPath,
  validateTrashDir,
} from "../src/lib/taskForm";
import {
  buildSyncPathPreview,
  resolveJobWritablePaths,
} from "../src/lib/syncPathPreview";
import type { NativeRemoteItem } from "../src/types";

const sampleRemotes: NativeRemoteItem[] = [
  {
    name: "MyOSS",
    type: "s3",
    summary: "",
    options: { provider: "Alibaba" },
    configuredSecrets: ["secret_access_key"],
    usedByJobs: [],
    usedByCrypts: [],
  },
  {
    name: "openlist",
    type: "webdav",
    summary: "",
    options: { url: "http://127.0.0.1:5244/dav", vendor: "other" },
    configuredSecrets: [],
    usedByJobs: [],
    usedByCrypts: ["cryptDav"],
  },
  {
    name: "cryptDav",
    type: "crypt",
    summary: "",
    options: { remote: "openlist:enc" },
    configuredSecrets: ["password"],
    usedByJobs: [],
    usedByCrypts: ["aliasCryptDav"],
  },
  {
    name: "aliasCryptDav",
    type: "alias",
    summary: "",
    options: { remote: "cryptDav:photos" },
    configuredSecrets: [],
    usedByJobs: [],
    usedByCrypts: [],
  },
  {
    name: "mirrorHttp",
    type: "http",
    summary: "",
    options: { url: "https://mirrors.example.com" },
    configuredSecrets: [],
    usedByJobs: [],
    usedByCrypts: [],
  },
  {
    name: "nextcloudDav",
    type: "webdav",
    summary: "",
    options: { url: "https://nc.example.com/dav", vendor: "nextcloud" },
    configuredSecrets: [],
    usedByJobs: [],
    usedByCrypts: [],
  },
  {
    name: "comboStore",
    type: "combine",
    summary: "",
    options: { upstreams: "dav=openlist: oss=MyOSS:bucket" },
    configuredSecrets: [],
    usedByJobs: [],
    usedByCrypts: [],
  },
];

describe("全流程审计修复回归测试", () => {
  it("S3 编辑缺少 provider 时默认 Other 而非静默改成 Alibaba，且 SFTP 不再下发冲突 shell_type (#8, #9)", () => {
    const s3Draft = draftFromRemote({
      name: "generic_s3",
      type: "s3",
      summary: "",
      options: { endpoint: "https://s3.example.com", access_key_id: "ak" },
      configuredSecrets: ["secret_access_key"],
      usedByJobs: [],
      usedByCrypts: [],
    });
    expect(s3Draft.provider).toBe("Other");
    expect(buildRemoteOptions(s3Draft).provider).toBe("Other");

    const sftpDraft = draftFromRemote({
      name: "nas_sftp",
      type: "sftp",
      summary: "",
      options: { host: "10.0.0.2", user: "root", shell_type: "powershell" },
      configuredSecrets: ["pass"],
      usedByJobs: [],
      usedByCrypts: [],
    });
    expect(buildRemoteOptions(sftpDraft).shell_type).toBeUndefined();
  });

  it("存储名称校验匹配后端正则，combine 快捷追加支持中文、去重和空格引号 (#14)", () => {
    expect(validateRemoteName("valid_name-1.0")).toBe("");
    expect(validateRemoteName("中文存储")).toBe("");
    expect(validateRemoteName("bad/name")).not.toBe("");
    expect(validateRemoteName("bad:name")).not.toBe("");
    expect(validateRemoteName("-leading-dash")).not.toBe("");
    expect(validateRemoteName("本地")).not.toBe("");

    let ups = appendCombineUpstream("", { value: "备份盘:", kind: "remote" });
    expect(ups).toBe("备份盘=备份盘:");
    ups = appendCombineUpstream(ups, {
      value: "/vol1/1000/My Photos",
      kind: "local",
    });
    expect(ups).toBe('备份盘=备份盘: "MyPhotos=/vol1/1000/My Photos"');
    ups = appendCombineUpstream(ups, {
      value: "/vol2/1000/My Photos",
      kind: "local",
    });
    expect(ups).toBe(
      '备份盘=备份盘: "MyPhotos=/vol1/1000/My Photos" "MyPhotos_2=/vol2/1000/My Photos"',
    );
  });

  it("目录树禁用不可同步根节点且手输路径执行完整校验与大小写规范化 (#11)", () => {
    expect(isPathDisabledInPicker("/", sampleRemotes)).toBe(true);
    expect(isPathDisabledInPicker("/本地", sampleRemotes)).toBe(true);
    expect(isPathDisabledInPicker("/MyOSS", sampleRemotes)).toBe(true);
    expect(isPathDisabledInPicker("/MyOSS/bucket", sampleRemotes)).toBe(false);
    expect(isPathDisabledInPicker("/openlist", sampleRemotes)).toBe(false);

    expect(validateManualSyncPath("/本地", sampleRemotes).error).not.toBe("");
    expect(validateManualSyncPath("/vol1/photos", sampleRemotes).error).not.toBe("");
    expect(validateManualSyncPath("/MyOSS", sampleRemotes).error).toContain("存储桶");
    expect(validateManualSyncPath("/MyOSS/bucket/../etc", sampleRemotes).error).not.toBe("");
    expect(
      validateManualSyncPath("/mirrorHttp/files", sampleRemotes, { role: "dst", method: 0 })
        .error,
    ).toContain("只读");
    expect(
      validateManualSyncPath("/mirrorHttp/files", sampleRemotes, { role: "src", method: 2 })
        .error,
    ).toContain("只读");

    // Case-insensitive remote canonicalization
    const ok = validateManualSyncPath("/myoss/bucket/sub/", sampleRemotes, {
      role: "dst",
      method: 0,
    });
    expect(ok).toEqual({ error: "", normalized: "/MyOSS/bucket/sub" });
  });

  it("隐藏的非法 trashDir 在关闭回收站或移动模式下不阻断保存，开启时校验全部非法字符 (#12)", () => {
    expect(validateTrashDir(".opensync-trash")).toBe("");
    expect(validateTrashDir("/abs")).not.toBe("");
    expect(validateTrashDir("bad*dir")).not.toBe("");
    expect(validateTrashDir("a/../b")).not.toBe("");

    const moveForm = {
      ...defaultJobForm(1),
      remark: "移动",
      method: 2,
      srcPath: ["/openlist/src"],
      dstPath: ["/MyOSS/bucket/dst"],
      trashEnable: true,
      trashDir: "/bad*trash",
    };
    expect(validateJobFormFields(moveForm, { remotes: sampleRemotes }).stepErrors[0]).toBe("");
    const payload = buildJobPayload(moveForm);
    expect(payload.trashEnable).toBe(0);
    expect(payload.trashDir).toBe(".opensync-trash");
  });

  it("穿透包装层检测标准 WebDAV 存储，且目标为 OpenList 时自动默认仅大小比对 (#13)", () => {
    expect(
      findStandardWebDAVRemotesForPaths(["/aliasCryptDav/2026"], sampleRemotes),
    ).toEqual(["aliasCryptDav"]);
    expect(
      findStandardWebDAVRemotesForPaths(["/cryptDav/sub"], sampleRemotes),
    ).toEqual(["cryptDav"]);
    expect(
      findStandardWebDAVRemotesForPaths(["/comboStore/dav/photos"], sampleRemotes),
    ).toEqual(["comboStore"]);
    expect(
      findStandardWebDAVRemotesForPaths(["/comboStore/oss/photos"], sampleRemotes),
    ).toEqual([]);
    expect(
      findStandardWebDAVRemotesForPaths(["/nextcloudDav/photos"], sampleRemotes),
    ).toEqual([]);
    expect(
      findStandardWebDAVRemotesForPaths(["/MyOSS/bucket/sub"], sampleRemotes),
    ).toEqual([]);

    // 1. Auto-select size-only (1) when destination resolves to OpenList / standard WebDAV
    const toOpenList = resolveAutoCompareModeForDst({
      dstPaths: ["/openlist/quark/photos"],
      remotes: sampleRemotes,
      currentCompareMode: 0,
      source: "default",
    });
    expect(toOpenList).toEqual({
      compareMode: 1,
      source: "auto",
      matchedDstRemotes: ["openlist"],
    });

    const toCryptOpenList = resolveAutoCompareModeForDst({
      dstPaths: ["/aliasCryptDav/2026"],
      remotes: sampleRemotes,
      currentCompareMode: 0,
      source: "default",
    });
    expect(toCryptOpenList.compareMode).toBe(1);
    expect(toCryptOpenList.source).toBe("auto");

    // 2. Revert back to default (0) if user switches destination to S3/Nextcloud without manual edit
    const backToOSS = resolveAutoCompareModeForDst({
      dstPaths: ["/MyOSS/bucket/photos"],
      remotes: sampleRemotes,
      currentCompareMode: toOpenList.compareMode,
      source: toOpenList.source,
    });
    expect(backToOSS).toEqual({
      compareMode: 0,
      source: "default",
      matchedDstRemotes: [],
    });

    const toNextcloud = resolveAutoCompareModeForDst({
      dstPaths: ["/nextcloudDav/photos"],
      remotes: sampleRemotes,
      currentCompareMode: 0,
      source: "default",
    });
    expect(toNextcloud).toEqual({
      compareMode: 0,
      source: "default",
      matchedDstRemotes: [],
    });

    // 3. Respect explicit manual choice (never override user's selection)
    const manualMD5 = resolveAutoCompareModeForDst({
      dstPaths: ["/openlist/quark/photos"],
      remotes: sampleRemotes,
      currentCompareMode: 2,
      source: "manual",
    });
    expect(manualMD5).toEqual({
      compareMode: 2,
      source: "manual",
      matchedDstRemotes: ["openlist"],
    });
  });

  it("移动模式源目录纳入跨任务写入冲突检测 (#7)", () => {
    const existingMoveJob = {
      id: 10,
      alistId: 1,
      remark: "移动入库",
      method: 2,
      srcPath: '["/openlist/inbox"]',
      dstPath: '["/MyOSS/bucket/archive"]',
    };
    expect(resolveJobWritablePaths(existingMoveJob)).toEqual([
      "/MyOSS/bucket/archive/",
      "/openlist/inbox/",
    ]);

    const previewWriteToMoveSrc = buildSyncPathPreview({
      srcPaths: ["/openlist/other"],
      dstPaths: ["/openlist/inbox"],
      method: 0,
      engineId: 1,
      existingJobs: [existingMoveJob],
    });
    expect(previewWriteToMoveSrc.conflicts.length).toBeGreaterThan(0);

    const previewMoveSameSrc = buildSyncPathPreview({
      srcPaths: ["/openlist/inbox/sub"],
      dstPaths: ["/MyOSS/bucket/archive2"],
      method: 2,
      engineId: 1,
      existingJobs: [existingMoveJob],
    });
    expect(previewMoveSameSrc.conflicts[0]).toContain("移动模式源目录");
  });
});
