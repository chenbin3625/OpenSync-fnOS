import { expect, test } from "@playwright/test";

// The realtime page prefers the progress SSE stream and falls back to polling.
// This spec replaces window.EventSource with a controllable fake so the
// consumer's wiring (URL, message parsing, snapshot application, fallback) is
// covered deterministically, without a real running task.

const activeTask = {
  taskId: 77,
  scanFinish: true,
  createTime: 1,
  duration: 1,
  num: { wait: 0, running: 1, success: 0, fail: 0, other: 0 },
  size: { wait: 0, running: 1, success: 0, fail: 0, other: 0 },
  doneSize: 0,
  remainSize: 1,
  speed: 1,
  speedAvg: 1,
  remainTime: 1,
};

const streamSnapshot = {
  ...activeTask,
  num: { wait: 0, running: 1, success: 7, fail: 0, other: 0 },
  size: { wait: 0, running: 1, success: 7, fail: 0, other: 0 },
  doneSize: 7,
};

test("realtime page consumes the progress SSE stream", async ({ page }) => {
  let streamUrl = "";
  await page.addInitScript(() => {
    type FakeHandler = (event: MessageEvent) => void;
    class FakeEventSource {
      static CONNECTING = 0;
      static OPEN = 1;
      static CLOSED = 2;
      url: string;
      readyState = 0;
      onopen: ((event: Event) => void) | null = null;
      onmessage: ((event: MessageEvent) => void) | null = null;
      onerror: ((event: Event) => void) | null = null;
      handlers: Record<string, FakeHandler[]> = {};
      closed = false;
      constructor(url: string) {
        this.url = url;
        (window as unknown as { __streamSources: FakeEventSource[] })
          .__streamSources ||= [];
        (
          window as unknown as { __streamSources: FakeEventSource[] }
        ).__streamSources.push(this);
        setTimeout(() => {
          this.readyState = 1;
          this.onopen?.(new Event("open"));
        }, 0);
      }
      addEventListener(type: string, handler: FakeHandler) {
        (this.handlers[type] ||= []).push(handler);
      }
      removeEventListener(type: string, handler: FakeHandler) {
        this.handlers[type] = (this.handlers[type] || []).filter(
          (item) => item !== handler,
        );
      }
      close() {
        this.closed = true;
        this.readyState = 2;
      }
      emit(data: unknown) {
        const event = { data: JSON.stringify(data) } as MessageEvent;
        this.onmessage?.(event);
        for (const handler of this.handlers.message || []) handler(event);
      }
    }
    Object.defineProperty(window, "EventSource", {
      configurable: true,
      value: FakeEventSource,
    });
  });

  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "success" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/version/latest"))
      return success({
        latestVersion: "v0.0.0",
        releaseURL: "https://example.test/releases",
        hasUpdate: false,
      });
    if (url.pathname.endsWith("/alist")) return success([]);
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      return success(activeTask);
    }
    if (url.pathname.endsWith("/job")) {
      return success({ dataList: [{ id: 1, enable: 1, remark: "SSE 任务" }], count: 1 });
    }
    return success(null);
  });

  await page.goto("/app/opensync/tasks?jobId=1&tab=realtime");
  await expect(page.getByRole("heading", { name: "正在同步" })).toBeVisible();

  // The consumer must connect to the backend's SSE route and ask for this job.
  await expect
    .poll(async () => {
      const sources = await page.evaluate(
        () =>
          (
            window as unknown as {
              __streamSources: { url: string }[];
            }
          ).__streamSources?.map((source) => source.url) || [],
      );
      return sources.find((url) => url.includes("/svr/job/stream")) || "";
    })
    .not.toBe("");

  streamUrl = await page.evaluate(() => {
    const sources = (
      window as unknown as {
        __streamSources: { url: string; closed: boolean }[];
      }
    ).__streamSources;
    return sources.find((source) => source.url.includes("/svr/job/stream"))!
      .url;
  });
  const parsed = new URL(streamUrl, "http://127.0.0.1");
  expect(parsed.pathname).toBe("/app/opensync/svr/job/stream");
  expect(parsed.searchParams.get("jobId")).toBe("1");

  // A snapshot on the stream replaces the polled state: the success tab count
  // changes from 0 (poll response) to 7 (stream snapshot).
  await page.evaluate(
    ({ payload }) => {
      const sources = (
        window as unknown as {
          __streamSources: { url: string; closed: boolean; emit: (data: unknown) => void }[];
        }
      ).__streamSources;
      sources
        .filter((source) => source.url.includes("/svr/job/stream") && !source.closed)
        .at(-1)!
        .emit(payload);
    },
    { payload: { code: 200, data: streamSnapshot, msg: "success" } },
  );
  await expect(page.getByRole("tab", { name: "成功 7" })).toBeVisible();

  // Navigating away must close the EventSource instead of leaking it.
  await page.goto("/app/opensync/settings");
  await expect
    .poll(() =>
      page.evaluate(
        () =>
          (
            window as unknown as { __streamSources?: { closed: boolean }[] }
          ).__streamSources?.every((source) => source.closed) ?? true,
      ),
    )
    .toBe(true);
});

test("realtime page falls back to polling when EventSource is unavailable", async ({
  page,
}) => {
  await page.addInitScript(() => {
    Object.defineProperty(window, "EventSource", {
      configurable: true,
      value: undefined,
    });
  });
  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "success" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/version/latest"))
      return success({
        latestVersion: "v0.0.0",
        releaseURL: "https://example.test/releases",
        hasUpdate: false,
      });
    if (url.pathname.endsWith("/alist")) return success([]);
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      return success(activeTask);
    }
    if (url.pathname.endsWith("/job")) {
      return success({ dataList: [{ id: 1, enable: 1, remark: "轮询任务" }], count: 1 });
    }
    return success(null);
  });

  await page.goto("/app/opensync/tasks?jobId=1&tab=realtime");
  await expect(page.getByRole("heading", { name: "正在同步" })).toBeVisible();
  await expect(page.getByRole("tab", { name: /成功/ })).toBeVisible();
});
