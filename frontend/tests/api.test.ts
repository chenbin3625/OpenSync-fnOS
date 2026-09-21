import { afterEach, describe, expect, it, vi } from "vitest";
import { api, serializeParams, apiBase, request } from "../src/api/client";
import { jobGetTaskCurrent } from "../src/api/job";

afterEach(() => vi.unstubAllGlobals());

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
});
