import { describe, expect, it } from "vitest";
import { createMockStore } from "../src/mocks/store";

describe("mock store", () => {
  it("uses a task timeout in hours within the settings form range", () => {
    const settings = createMockStore().getSettings();
    expect(settings.taskTimeout).toBe(48);
  });

  it("paginates jobs using the same page contract as the backend", () => {
    const store = createMockStore({
      jobs: [
        { id: 1, remark: "任务一" },
        { id: 2, remark: "任务二" },
        { id: 3, remark: "任务三" },
      ],
    });

    expect(store.listJobs(2, 2)).toMatchObject({
      dataList: [{ id: 3, remark: "任务三" }],
      count: 3,
    });
  });

  it("creates, updates, and deletes jobs in memory", () => {
    const store = createMockStore({ jobs: [] });
    const created = store.saveJob({
      remark: "新任务",
      alistId: 1,
      srcPath: ["/Photos"],
      dstPath: ["/Backup"],
      method: 0,
      isCron: 2,
      enable: 1,
    });

    expect(created.remark).toBe("新任务");
    expect(store.listJobs(1, 20).dataList).toHaveLength(1);

    store.saveJob({
      id: created.id,
      remark: "修改后的任务",
      alistId: 1,
      srcPath: ["/Photos"],
      dstPath: ["/Archive"],
      method: 1,
      isCron: 2,
      enable: 1,
    });
    expect(store.listJobs(1, 20).dataList[0]).toMatchObject({
      id: created.id,
      remark: "修改后的任务",
      dstPath: JSON.stringify(["/Archive"]),
      method: 1,
    });

    store.deleteJob(created.id);
    expect(store.listJobs(1, 20).dataList).toEqual([]);
  });

  it("keeps the same history entry when a running task stops", () => {
    const store = createMockStore({
      jobs: [{ id: 7, remark: "可执行任务", enable: 1 }],
    });

    expect(store.getCurrentTask(7)).toBeNull();
    const running = store.runJob(7);
    expect(store.getCurrentTask(7)?.taskId).toBe(running.taskId);
    expect(store.listHistory(7, { statusIn: [1, 2, 4] })).toMatchObject({
      dataList: [{ id: running.taskId, status: 1 }],
      count: 1,
    });
    expect(store.listHistory(7, { statusIn: [2, 4] }).count).toBe(0);
    expect(store.removeTaskRecord(running.taskId)).toBe("running");

    store.stopTask(running.taskId);
    expect(store.getCurrentTask(7)).toBeNull();
    expect(store.listHistory(7)).toMatchObject({
      dataList: [{ id: running.taskId, status: 4 }],
      count: 1,
    });
  });

  it("runs mirror sync jobs directly without staging deferred deletion approval", () => {
    const store = createMockStore({
      jobs: [
        {
          id: 8,
          remark: "全量任务",
          method: 1,
          enable: 1,
          srcPath: ["/Photos"],
          dstPath: ["/Backup"],
        },
      ],
    });
    const running = store.runJob(8);
    expect(store.getCurrentTask(8)?.taskId).toBe(running.taskId);
  });

  it("filters history by terminal status and an exclusive end boundary", () => {
    const store = createMockStore({
      jobs: [{ id: 3, remark: "历史过滤", enable: 1 }],
      history: {
        3: [
          { id: 101, status: 2, createTime: 1_700_000_000 },
          { id: 102, status: 7, createTime: 1_700_000_100 },
          { id: 103, status: 4, createTime: 1_700_000_200 },
        ],
      },
    });

    const statusIn = store.listHistory(3, {
      statusIn: [2, 7],
      pageNum: 1,
      pageSize: 20,
    });
    expect(statusIn.dataList.map((record) => record.id)).toEqual([101, 102]);

    const ranged = store.listHistory(3, {
      startTime: 1_700_000_050,
      endTimeExclusive: 1_700_000_200,
      pageNum: 1,
      pageSize: 20,
    });
    expect(ranged.dataList.map((record) => record.id)).toEqual([102]);
    expect(ranged.count).toBe(1);
  });

  it("returns a page scoped to the requested realtime status", () => {
    const store = createMockStore({
      jobs: [{ id: 9, remark: "实时任务", enable: 1 }],
    });
    store.runJob(9);
    const task = store.getCurrentTask(9)!;

    const page = store.listCurrentItems(9, {
      taskId: task.taskId,
      createTime: task.createTime,
      status: 1,
      pageNum: 2,
      pageSize: 10,
    });

    expect(page.taskId).toBe(task.taskId);
    expect(page.createTime).toBe(task.createTime);
    expect(page.status).toBe(1);
    expect(page.pageNum).toBe(2);
    expect(page.pageSize).toBe(10);
    expect(page.stale).toBe(false);
    expect(page.dataList).toHaveLength(10);
    expect(page.dataList.every((item) => item.status === 1)).toBe(true);
  });

  it("supports enable_all mirror preview guard and recent task items filtered by storage", () => {
    const store = createMockStore({
      engines: [{ id: 1, kind: "rclone", remark: "内置存储", url: "rclone://native" }],
      jobs: [
        {
          id: 1,
          enable: 0,
          method: 0,
          isCron: 3,
          alistId: 1,
          remark: "自动备份",
          srcPath: ["/本地/Photos"],
          dstPath: ["/Aliyun/Photos"],
        },
        {
          id: 2,
          enable: 0,
          method: 1,
          isCron: 3,
          alistId: 1,
          remark: "未预览镜像",
          srcPath: ["/本地/Media"],
          dstPath: ["/Aliyun/Media"],
        },
        {
          id: 3,
          enable: 1,
          method: 0,
          isCron: 2,
          alistId: 1,
          remark: "本地互备",
          srcPath: ["/本地/A"],
          dstPath: ["/本地/B"],
        },
      ],
      history: {
        1: [{ id: 501, status: 2, createTime: 1_700_000_010 }],
        3: [{ id: 503, status: 7, createTime: 1_700_000_020 }],
      },
      taskItems: {
        501: [
          {
            id: 11,
            taskId: 501,
            srcPath: "/本地/Photos/1.jpg",
            dstPath: "/Aliyun/Photos/1.jpg",
            fileName: "1.jpg",
            fileSize: 100,
            isPath: 0,
            type: 0,
            status: 2,
            createTime: 1_700_000_010,
          },
        ],
        503: [
          {
            id: 12,
            taskId: 503,
            srcPath: "/本地/A/doc.txt",
            dstPath: "/本地/B/doc.txt",
            fileName: "doc.txt",
            fileSize: 200,
            isPath: 0,
            type: 0,
            status: 7,
            errMsg: "disk full",
            createTime: 1_700_000_020,
          },
        ],
      },
    });

    const enableRes = store.batchJobAction("enable_all");
    expect(enableRes.succeeded).toBe(1);
    expect(enableRes.skipped).toBe(1);
    expect(enableRes.skippedJobs?.[0]?.jobId).toBe(2);
    expect(store.listJobs().dataList.find((j) => j.id === 1)?.enable).toBe(1);
    expect(store.listJobs().dataList.find((j) => j.id === 2)?.enable).toBe(0);

    const disableRes = store.batchJobAction("disable_all");
    expect(disableRes.succeeded).toBe(1);
    expect(store.listJobs().dataList.find((j) => j.id === 1)?.enable).toBe(0);

    const aliyunRecent = store.listRecentTaskItems({ storage: "Aliyun" });
    expect(aliyunRecent.count).toBe(1);
    expect(aliyunRecent.dataList[0].jobName).toBe("自动备份");
    expect(aliyunRecent.stats.successNum).toBe(1);

    const localRecent = store.listRecentTaskItems({ storage: "__local__" });
    expect(localRecent.count).toBe(1);
    expect(localRecent.dataList[0].jobName).toBe("本地互备");
    expect(localRecent.stats.errorNum).toBe(1);
  });
});
