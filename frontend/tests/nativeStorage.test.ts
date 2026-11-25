import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { setupServer } from "msw/node";
import { handlers, resetMockStore } from "../src/mocks/handlers";
import type {
  NativeImportPreview,
  NativeImportResult,
  NativeStorageState,
  NativeStorageTestResult,
} from "../src/types";

const server = setupServer(...handlers);
const apiUrl = "http://127.0.0.1/app/opensync/svr";

async function json<T>(response: Response): Promise<{ code: number; data: T; msg: string }> {
  return response.json() as Promise<{ code: number; data: T; msg: string }>;
}

beforeAll(() => server.listen({ onUnhandledRequest: "error" }));
afterEach(() => {
  server.resetHandlers();
  resetMockStore();
});
afterAll(() => server.close());

describe("native storage mock API lifecycle", () => {
  it("supports enabling engine, CRUD remotes with secret preservation, testing, and selective import", async () => {
    // 1. Initial state: disabled with authorized local roots
    const initial = await json<NativeStorageState>(
      await fetch(`${apiUrl}/native/storage`),
    );
    expect(initial.code).toBe(200);
    expect(initial.data.enabled).toBe(false);
    expect(initial.data.localRoots.length).toBeGreaterThan(0);

    // 2. Enable engine with custom remark and concurrency
    const enabled = await json<NativeStorageState>(
      await fetch(`${apiUrl}/native/engine`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ remark: "私有原生引擎", maxConcurrentJobs: 2 }),
      }),
    );
    expect(enabled.code).toBe(200);
    expect(enabled.data.enabled).toBe(true);
    expect(enabled.data.remark).toBe("私有原生引擎");
    expect(enabled.data.maxConcurrentJobs).toBe(2);

    // 3. Add S3 remote
    const added = await json<null>(
      await fetch(`${apiUrl}/native/storage`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: "Archive",
          options: {
            type: "s3",
            provider: "Alibaba",
            endpoint: "oss-cn-hangzhou.aliyuncs.com",
            access_key_id: "ak1",
            secret_access_key: "sk1",
          },
        }),
      }),
    );
    expect(added.code).toBe(200);

    // 4. State redacts secret_access_key and exposes configuredSecrets
    const afterAdd = await json<NativeStorageState>(
      await fetch(`${apiUrl}/native/storage`),
    );
    expect(afterAdd.data.remotes).toHaveLength(1);
    expect(afterAdd.data.remotes[0].options.secret_access_key).toBeUndefined();
    expect(afterAdd.data.remotes[0].configuredSecrets).toContain("secret_access_key");

    // 5. In-place edit keeps secret_access_key when omitted
    const edited = await json<null>(
      await fetch(`${apiUrl}/native/storage`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: "Archive",
          options: {
            type: "s3",
            provider: "Alibaba",
            endpoint: "oss-cn-beijing.aliyuncs.com",
            access_key_id: "ak2",
          },
        }),
      }),
    );
    expect(edited.code).toBe(200);
    const afterEdit = await json<NativeStorageState>(
      await fetch(`${apiUrl}/native/storage`),
    );
    expect(afterEdit.data.remotes[0].options.endpoint).toBe(
      "oss-cn-beijing.aliyuncs.com",
    );
    expect(afterEdit.data.remotes[0].configuredSecrets).toContain("secret_access_key");

    // 6. Test single storage
    const tested = await json<NativeStorageTestResult>(
      await fetch(`${apiUrl}/native/storage/test`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: "Archive" }),
      }),
    );
    expect(tested.code).toBe(200);
    expect(tested.data.ok).toBe(true);

    // 7. Preview + selective import rclone.conf (skips unsupported type cleanly)
    const sampleConf = [
      "[archive]",
      "type = s3",
      "provider = Alibaba",
      "endpoint = oss-cn-shanghai.aliyuncs.com",
      "access_key_id = ak3",
      "secret_access_key = sk3",
      "",
      "[dav]",
      "type = webdav",
      "url = https://dav.example.com/dav",
      "",
      "[bad_drive]",
      "type = onedrive",
    ].join("\n");

    const preview = await json<NativeImportPreview>(
      await fetch(`${apiUrl}/native/storage/import/preview`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ config: sampleConf }),
      }),
    );
    expect(preview.code).toBe(200);
    expect(preview.data.items).toHaveLength(3);
    expect(preview.data.items.find((i) => i.name === "archive")?.status).toBe("conflict");
    expect(preview.data.items.find((i) => i.name === "dav")?.status).toBe("ready");
    expect(preview.data.items.find((i) => i.name === "bad_drive")?.status).toBe("invalid");

    const imported = await json<NativeImportResult>(
      await fetch(`${apiUrl}/native/storage/import`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          config: sampleConf,
          selected: ["archive", "dav"],
          replace: true,
        }),
      }),
    );
    expect(imported.code).toBe(200);
    expect(imported.data.replaced).toEqual(["Archive"]);
    expect(imported.data.imported).toEqual(["dav"]);

    // 8. Delete single remote
    const deleted = await json<null>(
      await fetch(`${apiUrl}/native/storage?name=dav`, { method: "DELETE" }),
    );
    expect(deleted.code).toBe(200);
    const finalState = await json<NativeStorageState>(
      await fetch(`${apiUrl}/native/storage`),
    );
    expect(finalState.data.remotes.map((r) => r.name)).toEqual(["Archive"]);
    expect(finalState.data.rcloneVersion).toBeTruthy();
    expect(finalState.data.supportedTypes).toContain("union");
    expect(finalState.data.supportedTypes).toContain("b2");

    // 9. Rename, export, about, browse, restore, and job consistency check
    const renamed = await json<{ oldName: string; newName: string; updatedJobs: number }>(
      await fetch(`${apiUrl}/native/storage/rename`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ oldName: "Archive", newName: "ArchiveV2" }),
      }),
    );
    expect(renamed.code).toBe(200);
    expect(renamed.data.newName).toBe("ArchiveV2");

    const exported = await json<{ content: string; encrypted: boolean; filename: string }>(
      await fetch(`${apiUrl}/native/storage/export`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ names: ["ArchiveV2"], password: "" }),
      }),
    );
    expect(exported.code).toBe(200);
    expect(exported.data.content).toContain("[ArchiveV2]");
    expect(exported.data.encrypted).toBe(false);

    const about = await json<{ supported: boolean; total?: number }>(
      await fetch(`${apiUrl}/native/storage/about`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: "ArchiveV2" }),
      }),
    );
    expect(about.code).toBe(200);
    expect(about.data.supported).toBe(true);
    expect(about.data.total).toBeGreaterThan(0);

    const browse = await json<{ path: string; entries: Array<{ name: string; path: string; isDir: boolean }> }>(
      await fetch(`${apiUrl}/native/storage/browse`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ path: "/ArchiveV2" }),
      }),
    );
    expect(browse.code).toBe(200);
    expect(browse.data.entries.length).toBeGreaterThan(0);

    const restored = await json<null>(
      await fetch(`${apiUrl}/native/storage/restore`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          srcPath: "/ArchiveV2/backup-report.txt",
          dstLocalDir: "/vol1/1000/Photos",
          isDir: false,
          overwrite: false,
        }),
      }),
    );
    expect(restored.code).toBe(200);

    // 10. Create a bisync job with rclone tuning options and run consistency check
    const createdJob = await json<{ id: number }>(
      await fetch(`${apiUrl}/job`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          remark: "双向同步测试",
          alistId: enabled.data.engineId,
          method: 3,
          isCron: 2,
          srcPath: ["/本地/Photos"],
          dstPath: ["/ArchiveV2/Photos"],
          bwLimit: "10M",
          tpsLimit: 12,
          modifyWindow: "2s",
          fastList: 1,
          multiThreadStreams: 8,
          createEmptySrcDirs: 1,
          followSymlinks: 1,
          extraFlags: JSON.stringify({ checkFirst: true, s3NoHead: true }),
        }),
      }),
    );
    expect(createdJob.code).toBe(200);
    expect(createdJob.data.id).toBeGreaterThan(0);

    const checkRes = await json<{ status: string; matchedCount: number; download: boolean }>(
      await fetch(`${apiUrl}/job/check`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ id: createdJob.data.id, download: true }),
      }),
    );
    expect(checkRes.code).toBe(200);
    expect(checkRes.data.status).toBe("completed");
    expect(checkRes.data.download).toBe(true);
    expect(checkRes.data.matchedCount).toBeGreaterThan(0);
  });
});
