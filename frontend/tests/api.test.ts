import { afterEach, describe, expect, it, vi } from "vitest";
import {
  api,
  combineSignals,
  serializeParams,
  apiBase,
  request,
  requestTimeoutMs,
  sessionExpiredEvent,
  timeoutMessage,
} from "../src/api/client";
import { jobGetTaskCurrent } from "../src/api/job";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

// vitest runs in node, where window does not exist; request() dispatches the
// session-expired event on it.
function stubWindow() {
  const target = new EventTarget();
  const dispatched: string[] = [];
  vi.stubGlobal("window", {
    addEventListener: target.addEventListener.bind(target),
    removeEventListener: target.removeEventListener.bind(target),
    dispatchEvent: (event: Event) => {
      dispatched.push(event.type);
      return target.dispatchEvent(event);
    },
  });
  return dispatched;
}

function unauthorized(body: () => Promise<unknown>) {
  return vi.fn().mockResolvedValue({ ok: false, status: 401, json: body });
}

describe("gateway-aware API requests", () => {
  it("keeps query arrays and boolean zero values", () => {
    expect(
      serializeParams({
        statusIn: [2, 3, 8],
        hasError: 0,
        keyword: "a & b",
        empty: "",
      }),
    ).toBe("statusIn=2&statusIn=3&statusIn=8&hasError=0&keyword=a+%26+b");
  });
  it("keeps application requests under the stable gateway prefix", () => {
    expect(apiBase).toBe("/app/opensync/svr");
  });
  it("loads every task page for the sidebar menu", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          code: 200,
          data: { dataList: [{ id: 1 }], count: 501 },
          msg: "",
        }),
      })
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          code: 200,
          data: { dataList: [{ id: 501 }], count: 501 },
          msg: "",
        }),
      });
    vi.stubGlobal("fetch", fetchMock);

    const result = await api.jobMenu();

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(result.dataList).toEqual([{ id: 1 }, { id: 501 }]);
  });
  it("tests an existing engine through the dedicated endpoint", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ code: 200, data: null, msg: "" }),
    });
    vi.stubGlobal("fetch", fetchMock);

    await api.testEngine(7);

    expect(fetchMock).toHaveBeenCalledWith(
      `${apiBase}/alist/test?id=7`,
      expect.objectContaining({ method: "POST" }),
    );
  });
  it("disables HTTP caching for realtime task requests", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ code: 200, data: null, msg: "" }),
    });
    vi.stubGlobal("fetch", fetchMock);

    await jobGetTaskCurrent({ id: 1, current: 1, status: 2 });

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining(`${apiBase}/job?`),
      expect.objectContaining({ cache: "no-store" }),
    );
  });
  it("does not replace backend failures with development fallback data", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      status: 500,
      json: async () => ({ code: 500, data: null, msg: "真实后端错误" }),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(request("/job")).rejects.toThrow("真实后端错误");
  });
  it("rejects a backend business error returned as HTTP 200 + code 500", async () => {
    // respondError in backend/internal/handler/validation.go answers with HTTP
    // 200 and a 500 envelope, so the client must not treat `ok: true` as success.
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => ({ code: 500, data: null, msg: "源目录不能与目标目录重叠" }),
      }),
    );

    await expect(request("/job")).rejects.toThrow("源目录不能与目标目录重叠");
  });
  it("includes the HTTP status when the error body is not the API envelope", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: false,
        status: 429,
        json: async () => ({ error: "too many requests" }),
      }),
    );

    await expect(request("/job")).rejects.toThrow("HTTP 429");
  });
  it("returns the envelope data on success", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => ({ code: 200, data: { dataList: [], count: 0 }, msg: "success" }),
      }),
    );

    await expect(request("/job")).resolves.toEqual({ dataList: [], count: 0 });
  });

  it("treats a bodiless success (204 / empty 200) as success", async () => {
    const json = vi.fn(async () => {
      throw new SyntaxError("Unexpected end of JSON input");
    });
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, status: 204, json }),
    );
    await expect(request("/job", { method: "DELETE" })).resolves.toBeUndefined();

    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        status: 200,
        headers: new Headers({ "Content-Length": "0" }),
        json,
      }),
    );
    await expect(request("/job", { method: "DELETE" })).resolves.toBeUndefined();
    expect(json).not.toHaveBeenCalled();
  });
});

describe("session expiry", () => {
  it("does not dispatch session-expired for the /session request itself", async () => {
    // The listener re-requests /session; dispatching for it too looped forever
    // behind a spinner whenever the app was not opened from the fnOS desktop.
    const dispatched = stubWindow();
    vi.stubGlobal(
      "fetch",
      unauthorized(async () => ({ code: 401, msg: "请从飞牛桌面打开应用" })),
    );

    await expect(api.session()).rejects.toThrow("请从飞牛桌面打开应用");
    expect(dispatched).toEqual([]);
  });
  it("dispatches session-expired for other 401 responses", async () => {
    const dispatched = stubWindow();
    vi.stubGlobal(
      "fetch",
      unauthorized(async () => ({ code: 401, msg: "请从飞牛桌面打开应用" })),
    );

    await expect(request("/job")).rejects.toThrow("请从飞牛桌面打开应用");
    expect(dispatched).toEqual([sessionExpiredEvent]);
  });
  it("still treats a non-JSON 401 from a gateway as an expired session", async () => {
    const dispatched = stubWindow();
    vi.stubGlobal(
      "fetch",
      unauthorized(async () => {
        throw new SyntaxError("Unexpected token '<'");
      }),
    );

    const error = await request("/notify").catch((e: Error) => e);
    expect(error).toBeInstanceOf(Error);
    expect((error as Error).message).not.toBe("服务响应异常，请重试");
    expect((error as Error).message).toContain("飞牛桌面");
    expect(dispatched).toEqual([sessionExpiredEvent]);
  });
  it("reports the HTTP status for other non-JSON failures", async () => {
    stubWindow();
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: false,
        status: 502,
        json: async () => {
          throw new SyntaxError("Unexpected token '<'");
        },
      }),
    );

    await expect(request("/job")).rejects.toThrow("HTTP 502");
  });
});

describe("request signals", () => {
  it("maps the request timeout to a readable message", async () => {
    vi.useFakeTimers();
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (_url: string, init: RequestInit) =>
          new Promise((_resolve, reject) => {
            init.signal!.addEventListener("abort", () =>
              reject(new DOMException("aborted", "AbortError")),
            );
          }),
      ),
    );

    const pending = request("/job").catch((e: Error) => e);
    await vi.advanceTimersByTimeAsync(requestTimeoutMs);
    expect(((await pending) as Error).message).toBe(timeoutMessage);
  });
  it("keeps a caller abort as an abort, not a timeout", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (_url: string, init: RequestInit) =>
          new Promise((_resolve, reject) => {
            init.signal!.addEventListener("abort", () =>
              reject(new DOMException("aborted", "AbortError")),
            );
          }),
      ),
    );
    const controller = new AbortController();

    const pending = request("/job", { signal: controller.signal }).catch(
      (e: Error) => e,
    );
    controller.abort();
    const error = (await pending) as Error;
    expect(error.name).toBe("AbortError");
  });
  it("honours a per-call timeout override", async () => {
    vi.useFakeTimers();
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (_url: string, init: RequestInit) =>
          new Promise((_resolve, reject) => {
            init.signal!.addEventListener("abort", () =>
              reject(new DOMException("aborted", "AbortError")),
            );
          }),
      ),
    );

    const pending = request("/job", { timeoutMs: 1000 }).catch((e: Error) => e);
    await vi.advanceTimersByTimeAsync(999);
    expect(vi.getTimerCount()).toBeGreaterThan(0);
    await vi.advanceTimersByTimeAsync(1);
    expect(((await pending) as Error).message).toBe(timeoutMessage);
  });
  it("keeps a 130s window for deleting a job that is still stopping", async () => {
    // RemoveJobClient waits up to 2 minutes for the running task to stop, so a
    // delete that outlives the default 90s timeout is not a failure.
    vi.useFakeTimers();
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (_url: string, init: RequestInit) =>
          new Promise((_resolve, reject) => {
            init.signal!.addEventListener("abort", () =>
              reject(new DOMException("aborted", "AbortError")),
            );
          }),
      ),
    );

    const pending = api.deleteJob(7).catch((e: Error) => e);
    await vi.advanceTimersByTimeAsync(requestTimeoutMs);
    expect(vi.getTimerCount()).toBe(1);
    await vi.advanceTimersByTimeAsync(130000 - requestTimeoutMs);
    await expect(pending).resolves.toBeInstanceOf(Error);
    await expect(pending).resolves.toMatchObject({ message: timeoutMessage });
  });
  it("aborts the session request with the caller signal", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (_url: string, init: RequestInit) =>
          new Promise((_resolve, reject) => {
            init.signal!.addEventListener("abort", () =>
              reject(new DOMException("aborted", "AbortError")),
            );
          }),
      ),
    );
    const controller = new AbortController();

    const pending = api.session(controller.signal).catch((e: Error) => e);
    controller.abort();
    expect(((await pending) as Error).name).toBe("AbortError");
  });
  it("does not require AbortSignal.any or AbortSignal.timeout", async () => {
    // Older fnOS WebViews lack both; every request used to throw a TypeError.
    vi.stubGlobal(
      "AbortSignal",
      Object.assign(function AbortSignal() {}, { prototype: AbortSignal.prototype }),
    );
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ code: 200, data: [1], msg: "" }),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(
      request("/alist", { signal: new AbortController().signal }),
    ).resolves.toEqual([1]);
    expect(fetchMock.mock.calls[0][1].signal).toBeDefined();
  });
});

describe("combineSignals fallback", () => {
  function withoutAny<T>(run: () => T): T {
    const original = Object.getOwnPropertyDescriptor(AbortSignal, "any");
    Object.defineProperty(AbortSignal, "any", {
      value: undefined,
      configurable: true,
    });
    try {
      return run();
    } finally {
      if (original) Object.defineProperty(AbortSignal, "any", original);
      else delete (AbortSignal as { any?: unknown }).any;
    }
  }

  it("aborts when any input aborts and forwards the reason", () => {
    withoutAny(() => {
      const a = new AbortController();
      const b = new AbortController();
      const { signal } = combineSignals([a.signal, b.signal]);
      expect(signal.aborted).toBe(false);
      b.abort("stop");
      expect(signal.aborted).toBe(true);
      expect(signal.reason).toBe("stop");
    });
  });
  it("starts aborted when an input is already aborted", () => {
    withoutAny(() => {
      const a = new AbortController();
      a.abort("early");
      const { signal } = combineSignals([a.signal, new AbortController().signal]);
      expect(signal.aborted).toBe(true);
      expect(signal.reason).toBe("early");
    });
  });
  it("detaches its listeners on cleanup", () => {
    withoutAny(() => {
      const a = new AbortController();
      const b = new AbortController();
      const removeA = vi.spyOn(a.signal, "removeEventListener");
      const removeB = vi.spyOn(b.signal, "removeEventListener");
      const { signal, cleanup } = combineSignals([a.signal, b.signal]);
      cleanup();
      expect(removeA).toHaveBeenCalledWith("abort", expect.any(Function));
      expect(removeB).toHaveBeenCalledWith("abort", expect.any(Function));
      a.abort();
      expect(signal.aborted).toBe(false);
    });
  });
  it("passes a single signal through untouched", () => {
    const a = new AbortController();
    expect(combineSignals([a.signal, undefined]).signal).toBe(a.signal);
  });
});
