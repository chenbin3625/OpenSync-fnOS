import { afterEach, describe, expect, it, vi } from "vitest";
import { api, serializeParams, apiBase } from "../src/api/client";

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
});
