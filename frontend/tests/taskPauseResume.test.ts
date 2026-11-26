import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { setupServer } from "msw/node";
import { handlers, resetMockStore } from "../src/mocks/handlers";
import { calcRealtimeProgress } from "../src/pages/Home/taskRows";
import { buildJobPayload, jobToForm } from "../src/lib/taskForm";
import type { CurrentTaskData, DashboardOverview, JobItem, PageData } from "../src/types";

const server = setupServer(...handlers);
const base = "http://localhost/app/opensync/svr";

beforeAll(() => server.listen({ onUnhandledRequest: "error" }));
afterEach(() => server.resetHandlers());
afterAll(() => server.close());

async function call<T = unknown>(
  path: string,
  init?: RequestInit,
): Promise<{ status: number; body: { code: number; data: T; msg: string } }> {
  const response = await fetch(`${base}${path}`, init);
  return { status: response.status, body: await response.json() };
}

function jsonInit(method: string, body: unknown): RequestInit {
  return {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  };
}

describe("task-level pause and resume", () => {
  it("pauses and resumes a running task across current, job list, and dashboard endpoints", async () => {
    const store = resetMockStore({
      jobs: [{ id: 12, remark: "大文件同步", enable: 1 }],
    });

    // Start task
    const runRes = await call("/job", jsonInit("PUT", { id: "12" }));
    expect(runRes.body.code).toBe(200);
    const initial = store.getCurrentTask(12);
    expect(initial).not.toBeNull();
    expect(initial?.paused).toBeFalsy();

    // Pause task
    const pauseRes = await call(
      "/job",
      jsonInit("PUT", { taskId: String(initial!.taskId), action: "pause" }),
    );
    expect(pauseRes.body.code).toBe(200);

    const currentPaused = await call<CurrentTaskData>("/job?id=12&current=1");
    expect(currentPaused.body.data.paused).toBe(true);
    expect(currentPaused.body.data.speed).toBe(0);
    expect(currentPaused.body.data.remainTime).toBe(0);

    const jobsPaused = await call<PageData<JobItem>>("/job?pageNum=1&pageSize=20");
    const pausedJob = jobsPaused.body.data.dataList.find((j) => j.id === 12);
    expect(pausedJob?.running).toBe(true);
    expect(pausedJob?.paused).toBe(true);

    const dashPaused = await call<DashboardOverview>("/dashboard");
    const dashJob = dashPaused.body.data.jobs.find((j) => j.id === 12);
    expect(dashJob?.running).toBe(true);
    expect(dashJob?.paused).toBe(true);

    // Adjust live bandwidth limit while paused
    const bwRes = await call(
      "/job",
      jsonInit("PUT", {
        taskId: String(initial!.taskId),
        action: "bwlimit",
        bwLimit: "10M",
      }),
    );
    expect(bwRes.body.code).toBe(200);
    expect(store.getCurrentTask(12)?.liveBwLimit).toBe("10M");

    // Reject bwLimit paired with pause action
    const invalidRes = await call(
      "/job",
      jsonInit("PUT", {
        taskId: String(initial!.taskId),
        action: "pause",
        bwLimit: "10M",
      }),
    );
    expect(invalidRes.status).toBe(400);
    expect(invalidRes.body.code).toBe(500);

    // Resume task
    const resumeRes = await call(
      "/job",
      jsonInit("PUT", { taskId: String(initial!.taskId), action: "resume" }),
    );
    expect(resumeRes.body.code).toBe(200);

    const currentResumed = await call<CurrentTaskData>("/job?id=12&current=1");
    expect(currentResumed.body.data.paused).toBe(false);

    const jobsResumed = await call<PageData<JobItem>>("/job?pageNum=1&pageSize=20");
    expect(
      jobsResumed.body.data.dataList.find((j) => j.id === 12)?.paused,
    ).toBe(false);

    const dashResumed = await call<DashboardOverview>("/dashboard");
    expect(dashResumed.body.data.jobs.find((j) => j.id === 12)?.paused).toBe(
      false,
    );
  });

  it("forces speed and remainTime to 0 in calcRealtimeProgress when paused", () => {
    const progress = calcRealtimeProgress(
      {
        taskId: 101,
        scanFinish: true,
        createTime: 1000,
        duration: 20,
        firstSync: 1002,
        num: { wait: 1, running: 1, success: 0, fail: 0, other: 0 },
        size: { wait: 500, running: 500, success: 0, fail: 0, other: 0 },
        doneSize: 200,
        remainSize: 800,
        speed: 100,
        speedAvg: 50,
        remainTime: 16,
        paused: true,
      },
      { duration: 18, doneSize: 100 },
    );
    expect(progress.speed).toBe(0);
    expect(progress.remainTime).toBe(0);
    expect(progress.speedAvg).toBe(50);
  });

  it("strips runtime paused flag from buildJobPayload", () => {
    const job: JobItem = {
      id: 1,
      enable: 1,
      remark: "test",
      srcPath: '["/local/a"]',
      dstPath: '["/oss/b"]',
      alistId: 1,
      useCacheT: 0,
      useCacheS: 0,
      method: 0,
      interval: 60,
      isCron: 2,
      running: true,
      paused: true,
    };
    const form = { ...jobToForm(job), paused: true } as ReturnType<
      typeof jobToForm
    > & { paused?: boolean };
    const payload = buildJobPayload(form) as Record<string, unknown>;
    expect(payload).not.toHaveProperty("paused");
    expect(payload).not.toHaveProperty("running");
  });
});
