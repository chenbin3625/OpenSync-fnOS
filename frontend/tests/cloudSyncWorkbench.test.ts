import { afterEach, describe, expect, it, vi } from "vitest";
import { api, apiBase } from "../src/api/client";
import {
  syncStatusLabels,
  syncStatusTone,
} from "../src/lib/continuousSync";
import { analyzeDstEncryption } from "../src/lib/quickCrypt";
import {
  getJobMountNames,
  jobHasCryptRemote,
  resolveRemoteChain,
} from "../src/lib/taskForm";
import { createMockStore } from "../src/mocks/store";
import type { AlistItem, JobItem, NativeRemoteItem } from "../src/types";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Module 4 workbench helpers and API client", () => {
  it("extracts unique mount names while preserving order", () => {
    const mounts = getJobMountNames({
      srcPath: JSON.stringify(["/本地/Photos", "/本地/Documents"]),
      dstPath: JSON.stringify(["/AliyunDrive/Backups", "/AliyunDrive/Photos"]),
    });
    expect(mounts).toEqual(["本地", "AliyunDrive"]);
  });

  it("detects crypt remotes from both engine metadata and native remotes", () => {
    const engines: AlistItem[] = [
      {
        id: 1,
        kind: "rclone",
        remark: "内置存储",
        url: "rclone://native",
        userName: "rclone",
        remotes: { Aliyun: "webdav", CryptVault: "crypt" },
        remoteDetails: {
          WrappedCloud: { type: "s3", hasCrypt: "true" },
        },
        createTime: 0,
      },
    ];
    const remotes: NativeRemoteItem[] = [
      {
        name: "RemoteCrypt",
        type: "crypt",
        summary: "encrypted",
        options: {},
        usedByJobs: [],
        usedByCrypts: [],
        configuredSecrets: [],
      },
    ];

    expect(
      jobHasCryptRemote(
        { alistId: 1, srcPath: '["/本地/A"]', dstPath: '["/CryptVault/A"]' },
        engines,
        remotes,
      ),
    ).toBe(true);
    expect(
      jobHasCryptRemote(
        { alistId: 1, srcPath: '["/本地/A"]', dstPath: '["/WrappedCloud/A"]' },
        engines,
        remotes,
      ),
    ).toBe(true);
    expect(
      jobHasCryptRemote(
        { alistId: 1, srcPath: '["/本地/A"]', dstPath: '["/RemoteCrypt/A"]' },
        engines,
        remotes,
      ),
    ).toBe(true);
    expect(
      jobHasCryptRemote(
        { alistId: 1, srcPath: '["/本地/A"]', dstPath: '["/Aliyun/A"]' },
        engines,
        remotes,
      ),
    ).toBe(false);
  });

  it("serializes recentTaskItems params and global batch actions properly", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          code: 200,
          data: {
            dataList: [],
            count: 0,
            pageNum: 1,
            pageSize: 12,
            stats: {
              allNum: 0,
              waitNum: 0,
              runningNum: 0,
              successNum: 0,
              failNum: 0,
              otherNum: 0,
              deleteNum: 0,
              errorNum: 0,
              sumSize: 0,
            },
          },
          msg: "",
        }),
      })
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          code: 200,
          data: {
            action: "enable_all",
            succeeded: 1,
            failed: 0,
            skipped: 1,
            skippedJobs: [
              { jobId: 2, jobName: "镜像任务", reason: "未完成首次镜像影响预览" },
            ],
            results: [],
          },
          msg: "",
        }),
      });
    vi.stubGlobal("fetch", fetchMock);

    await api.recentTaskItems({
      pageNum: 1,
      pageSize: 12,
      jobIds: [1, 3],
      storage: "__local__",
      hasError: true,
    });
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      `${apiBase}/job/items/recent?pageNum=1&pageSize=12&jobIds=1%2C3&storage=__local__&hasError=1`,
    );

    const batchRes = await api.batchJobAction("enable_all");
    expect(batchRes.skipped).toBe(1);
    expect(batchRes.skippedJobs?.[0]?.jobId).toBe(2);
    expect(fetchMock.mock.calls[1]?.[0]).toBe(`${apiBase}/job/batch`);
    expect(JSON.parse(String(fetchMock.mock.calls[1]?.[1]?.body))).toEqual({
      action: "enable_all",
    });

    const unused: Pick<JobItem, "id"> = { id: 1 };
    expect(unused.id).toBe(1);
  });

  it("aligns idle weekly status, wrapper crypt resolution and MockStore hasCrypt metadata", () => {
    expect(syncStatusLabels.idle).toBe("按周历待命");
    expect(syncStatusTone("idle")).toBe("muted");

    const remotes: NativeRemoteItem[] = [
      {
        name: "rawS3",
        type: "s3",
        summary: "s3",
        options: {},
        configuredSecrets: [],
        usedByJobs: [],
        usedByCrypts: ["vaultCrypt"],
      },
      {
        name: "vaultCrypt",
        type: "crypt",
        summary: "crypt",
        options: { remote: "rawS3:bucket" },
        configuredSecrets: ["password"],
        usedByJobs: [],
        usedByCrypts: [],
      },
      {
        name: "vaultAlias",
        type: "alias",
        summary: "alias",
        options: { remote: "vaultCrypt:photos" },
        configuredSecrets: [],
        usedByJobs: [],
        usedByCrypts: [],
      },
    ];
    const chain = resolveRemoteChain("vaultAlias", remotes);
    expect(chain.hasCrypt).toBe(true);
    expect(chain.cryptRemoteName).toBe("vaultCrypt");
    const enc = analyzeDstEncryption(["/vaultAlias/2026"], remotes, 0);
    expect(enc.encrypted[0]?.cryptRemoteName).toBe("vaultCrypt");

    const store = createMockStore({
      engines: [{ id: 1, kind: "rclone", remark: "内置存储", url: "rclone://native" }],
    });
    expect(
      store.saveNativeRemote({
        name: "aliyun",
        options: { type: "webdav", url: "https://dav.example.com" },
      }),
    ).toBeNull();
    expect(
      store.saveNativeRemote({
        name: "aliyun-crypt",
        options: { type: "crypt", remote: "aliyun:Enc", password: "secret" },
      }),
    ).toBeNull();
    expect(
      store.saveNativeRemote({
        name: "aliyun-alias",
        options: { type: "alias", remote: "aliyun-crypt:sub" },
      }),
    ).toBeNull();
    const engines = store.listEngines();
    expect(engines[0]?.remoteDetails?.["aliyun-crypt"]?.hasCrypt).toBe("true");
    expect(engines[0]?.remoteDetails?.["aliyun-alias"]?.hasCrypt).toBe("true");
    expect(engines[0]?.remoteDetails?.aliyun?.hasCrypt).toBeUndefined();
  });
});
