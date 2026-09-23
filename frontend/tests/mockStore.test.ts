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

  it("starts and stops a job through task actions", () => {
    const store = createMockStore({
      jobs: [{ id: 7, remark: "可执行任务", enable: 1 }],
    });

    expect(store.getCurrentTask(7)).toBeNull();
    store.runJob(7);
    const running = store.getCurrentTask(7);
    expect(running).not.toBeNull();
    expect(running?.taskId).toBeGreaterThan(0);

    store.stopTask(running!.taskId);
    expect(store.getCurrentTask(7)).toBeNull();
    expect(store.listHistory(7, { pageNum: 1, pageSize: 20 }).count).toBe(1);
    expect(
      store.listHistory(7, { pageNum: 1, pageSize: 20 }).dataList[0].status,
    ).toBe(4);
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
});
