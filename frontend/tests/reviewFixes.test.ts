import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
vi.mock("@trimjs/web-app", () => ({
  TrimApp: class {},
}));
import { setupServer } from "msw/node";
import {
  applyExcludeTreeCheck,
  buildExcludeCheckedKeys,
  toExcludeRelativePath,
} from "../src/lib/excludeTree";
import { safeExternalURL } from "../src/lib/host";
import {
  addCustomFileTypeFilter,
  isExcludePatternEnabled,
  normalizeCustomFileTypePattern,
  parseExcludeFolders,
  parseOtherFileTypeFilters,
  updateExcludeFolders,
  updateExcludePatterns,
} from "../src/lib/taskForm";
import {
  buildNotifyParams,
  defaultBarkServer,
  notifyToForm,
  validateNotifyForm,
} from "../src/lib/notifyForm";
import { createMockStore } from "../src/mocks/store";
import { handlers, resetMockStore } from "../src/mocks/handlers";

const server = setupServer(...handlers);
const apiUrl = "http://127.0.0.1/app/opensync/svr";

beforeAll(() => server.listen({ onUnhandledRequest: "error" }));
afterEach(() => {
  server.resetHandlers();
  resetMockStore();
});
afterAll(() => server.close());

describe("excludeTree", () => {
  it("converts absolute paths to trailing-slash relative exclude paths", () => {
    expect(toExcludeRelativePath("/data", "/data/photos/raw")).toBe(
      "photos/raw/",
    );
    expect(toExcludeRelativePath("/data/", "/data/photos/")).toBe("photos/");
    expect(toExcludeRelativePath("/", "/photos/raw")).toBe("photos/raw/");
  });

  it("expands relative excluded paths into absolute keys for every source root", () => {
    expect(
      buildExcludeCheckedKeys(["/src1", "/src2/"], ["tmp/", "/cache/raw/"]),
    ).toEqual(["/src1/tmp", "/src2/tmp", "/src1/cache/raw", "/src2/cache/raw"]);
    expect(buildExcludeCheckedKeys(["/"], ["photos/", "/"])).toEqual([
      "/photos",
    ]);
  });

  it("preserves checked children when select-all includes the disabled root node", () => {
    expect(
      applyExcludeTreeCheck(
        ["/data"],
        [],
        ["/data", "/data/photos", "/data/docs", "/data/photos/raw"],
        "/data/photos",
      ),
    ).toEqual(["docs/", "photos/"]);

    expect(
      applyExcludeTreeCheck(
        ["/data"],
        [],
        ["/data", "/data/photos", "/data/docs"],
      ),
    ).toEqual(["docs/", "photos/"]);
  });

  it("removes a rule across multiple source roots when unchecked in one source", () => {
    const srcPaths = ["/src1", "/src2"];
    const prev = ["tmp/", "cache/"];

    // Clicking /src1/tmp unchecks it in /src1; /src2/tmp must not re-add it.
    expect(
      applyExcludeTreeCheck(
        srcPaths,
        prev,
        ["/src2/tmp", "/src1/cache", "/src2/cache"],
        "/src1/tmp",
      ),
    ).toEqual(["cache/"]);

    // Without clickedKey, missing in any root also removes the rule cleanly.
    expect(
      applyExcludeTreeCheck(srcPaths, prev, [
        "/src2/tmp",
        "/src1/cache",
        "/src2/cache",
      ]),
    ).toEqual(["cache/"]);
  });
});

describe("host safeExternalURL", () => {
  it("rejects unsafe protocols and empty or invalid hosts", () => {
    expect(safeExternalURL("javascript:alert(1)")).toBeNull();
    expect(safeExternalURL("JAVASCRIPT:alert(1)")).toBeNull();
    expect(safeExternalURL("data:text/html,<script>alert(1)</script>")).toBeNull();
    expect(safeExternalURL("file:///etc/passwd")).toBeNull();
    expect(safeExternalURL("ftp://example.com/file")).toBeNull();
    expect(safeExternalURL("http://")).toBeNull();
    expect(safeExternalURL("https://")).toBeNull();
    expect(safeExternalURL("")).toBeNull();
    expect(safeExternalURL("   ")).toBeNull();
    expect(safeExternalURL(null)).toBeNull();
    expect(safeExternalURL(undefined)).toBeNull();
    expect(safeExternalURL("/app/opensync")).toBeNull();
  });

  it("allows http and https URLs with valid hosts", () => {
    expect(safeExternalURL("http://10.0.0.1:5666/path?a=1")).toBe(
      "http://10.0.0.1:5666/path?a=1",
    );
    expect(safeExternalURL("  https://github.com/chenbin3625/OpenSync-fnOS  ")).toBe(
      "https://github.com/chenbin3625/OpenSync-fnOS",
    );
  });
});

describe("taskForm exclude rules", () => {
  it("retains #recycle/ system preset across exclude pattern and folder updates", () => {
    const withRecycle = updateExcludePatterns("", ["#recycle/"], true);
    expect(withRecycle).toBe("#recycle/");
    expect(isExcludePatternEnabled(withRecycle, "#recycle/")).toBe(true);
    expect(isExcludePatternEnabled("\\#recycle/", "#recycle/")).toBe(true);
    expect(isExcludePatternEnabled("# #recycle/", "#recycle/")).toBe(false);

    // #recycle/ is a system preset, so folder tree updates must not strip or swallow it.
    expect(parseExcludeFolders("#recycle/\nphotos/")).toEqual(["photos/"]);
    const updated = updateExcludeFolders("#recycle/", ["photos/"]);
    expect(updated).toContain("#recycle/");
    expect(updated).toContain("photos/");

    const clearedFolders = updateExcludeFolders(updated, []);
    expect(clearedFolders).toBe("#recycle/");
  });

  it("rejects trailing dots like *.txt. and negation patterns with !", () => {
    expect(normalizeCustomFileTypePattern("*.txt.")).toBe("");
    expect(normalizeCustomFileTypePattern("*.txt..")).toBe("");
    expect(normalizeCustomFileTypePattern("!*.txt")).toBe("");
    expect(normalizeCustomFileTypePattern("foo!bar")).toBe("");
    expect(normalizeCustomFileTypePattern("*.txt")).toBe("*.txt");

    expect(addCustomFileTypeFilter("", "*.txt.")).toBe("");
    expect(addCustomFileTypeFilter("", "!*.log")).toBe("");
    expect(parseOtherFileTypeFilters("*.txt.\n!*.log\n*.custom")).toEqual([
      { pattern: "*.custom", enabled: true },
    ]);
  });
});

describe("notifyForm null safety", () => {
  it("handles null params and null field values without 'null' coercion or TypeError", () => {
    const fromNullParams = notifyToForm({
      id: 1,
      method: 0,
      enable: 1,
      params: null as unknown as string,
    });
    expect(fromNullParams.url).toBe("");
    expect(fromNullParams.httpMethod).toBe("POST");
    expect(fromNullParams.contentType).toBe("application/json");
    expect(fromNullParams.headers).toBe("");
    expect(fromNullParams.body).toBe("");

    const fromNullFields = notifyToForm({
      id: 2,
      method: 0,
      enable: 1,
      params: JSON.stringify({
        url: null,
        httpMethod: null,
        contentType: null,
        titleName: null,
        contentName: null,
        body: null,
        headers: null,
        secret: null,
        sendKey: null,
      }),
    });
    expect(fromNullFields.url).toBe("");
    expect(fromNullFields.httpMethod).toBe("POST");
    expect(fromNullFields.contentType).toBe("application/json");
    expect(fromNullFields.titleName).toBe("title");
    expect(fromNullFields.contentName).toBe("content");
    expect(fromNullFields.body).toBe("");
    expect(fromNullFields.headers).toBe("");
    expect(fromNullFields.secret).toBe("");
    expect(fromNullFields.sendKey).toBe("");

    const barkFromNull = notifyToForm({
      id: 3,
      method: 5,
      enable: 1,
      params: JSON.stringify({
        server: null,
        deviceKey: null,
        group: null,
        sound: null,
      }),
    });
    expect(barkFromNull.server).toBe(defaultBarkServer);
    expect(barkFromNull.deviceKey).toBe("");
    expect(barkFromNull.group).toBeUndefined();
    expect(barkFromNull.sound).toBeUndefined();

    const wecomFromNull = notifyToForm({
      id: 4,
      method: 3,
      enable: 1,
      params: JSON.stringify({
        corpid: null,
        corpsecret: null,
        agentid: null,
        touser: null,
      }),
    });
    expect(wecomFromNull.corpid).toBe("");
    expect(wecomFromNull.corpsecret).toBe("");
    expect(wecomFromNull.agentid).toBe("");
    expect(wecomFromNull.touser).toBe("@all");

    const builtWebhook = buildNotifyParams({
      method: 0,
      enable: true,
      url: null as unknown as string,
      httpMethod: null as unknown as string,
      contentType: null as unknown as string,
      body: null as unknown as string,
      headers: null as unknown as string,
    });
    expect(builtWebhook).toMatchObject({
      url: undefined,
      httpMethod: "POST",
      contentType: "application/json",
      body: undefined,
      headers: undefined,
    });

    expect(
      buildNotifyParams({
        method: 1,
        enable: true,
        sendKey: null as unknown as string,
        version: null as unknown as string,
      }),
    ).toEqual({ notSendNull: false, sendKey: undefined, version: "v3" });

    expect(
      validateNotifyForm({
        method: 0,
        enable: true,
        url: null as unknown as string,
      }),
    ).toBe("请输入有效的 Webhook URL");
  });
});

describe("mocks store & handlers alignment", () => {
  it("redacts Bark server URL and guards deviceKey restoration when target changes", () => {
    const store = createMockStore({
      notifications: [
        {
          id: 1,
          method: 5,
          enable: 1,
          params: {
            server: "https://user:pass@bark.example.com/secret-token-path",
            deviceKey: "bark-device-secret-1234",
          },
        },
      ],
    });

    const listed = JSON.parse(store.listNotifications()[0].params) as {
      server: string;
      deviceKey: string;
    };
    expect(listed.server).not.toContain("pass");
    expect(listed.server).not.toContain("secret-token-path");
    expect(listed.server).toContain("bark.example.com");
    expect(listed.server).toContain("****");
    expect(listed.deviceKey).toBe("****1234");

    // Saving with masked server & masked deviceKey keeps stored secrets intact.
    store.updateNotification({
      id: 1,
      method: 5,
      enable: 1,
      params: { server: listed.server, deviceKey: listed.deviceKey, group: "ops" },
    });
    expect(store.getRawNotificationParams(1)).toContain("bark-device-secret-1234");
    expect(store.getRawNotificationParams(1)).toContain("secret-token-path");

    // Changing Bark server to a new target must NOT restore the old deviceKey.
    store.updateNotification({
      id: 1,
      method: 5,
      enable: 1,
      params: { server: "https://other-bark.example.com", deviceKey: "" },
    });
    const afterTargetChange = JSON.parse(
      store.getRawNotificationParams(1),
    ) as { server: string; deviceKey: string };
    expect(afterTargetChange.server).toBe("https://other-bark.example.com");
    expect(afterTargetChange.deviceKey).toBe("");
  });

  it("keeps empty headers: {} unredacted and allows clearing stored headers with {}", () => {
    const store = createMockStore({
      notifications: [
        {
          id: 1,
          method: 0,
          enable: 1,
          params: {
            url: "https://hook.example.com/notify",
            headers: {},
          },
        },
        {
          id: 2,
          method: 0,
          enable: 1,
          params: {
            url: "https://hook.example.com/notify",
            headers: { Authorization: "Bearer secret" },
          },
        },
      ],
    });

    const first = JSON.parse(store.listNotifications()[0].params) as {
      headers: Record<string, string>;
    };
    expect(first.headers).toEqual({});

    const second = JSON.parse(store.listNotifications()[1].params) as {
      headers: unknown;
    };
    expect(second.headers).toBe("****");

    // Submitting headers: {} clears previously saved headers rather than restoring them.
    store.updateNotification({
      id: 2,
      method: 0,
      enable: 1,
      params: { url: "****", headers: {} },
    });
    const updatedSecond = JSON.parse(
      store.getRawNotificationParams(2),
    ) as Record<string, unknown>;
    expect(updatedSecond.headers).toBeUndefined();
  });

  it("requires a valid positive integer ID on PUT /svr/notify", async () => {
    resetMockStore({
      notifications: [
        { id: 1, method: 1, enable: 1, params: { sendKey: "key-1" } },
      ],
    });

    const missingBody = await fetch(`${apiUrl}/notify`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({}),
    });
    expect(missingBody.status).toBe(400);
    expect(await missingBody.json()).toMatchObject({
      code: 500,
      msg: "缺少必要参数",
    });

    for (const invalidId of [undefined, 0, -1, 1.5, "abc"]) {
      const res = await fetch(`${apiUrl}/notify`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          notify: { id: invalidId, method: 1, enable: 1, params: {} },
        }),
      });
      expect(res.status).toBe(400);
      expect(await res.json()).toMatchObject({
        code: 500,
        msg: "缺少必要参数",
      });
    }

    const notFound = await fetch(`${apiUrl}/notify`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        notify: { id: 9999, method: 1, enable: 1, params: {} },
      }),
    });
    expect(notFound.status).toBe(400);
    expect(await notFound.json()).toMatchObject({
      code: 500,
      msg: "通知配置不存在",
    });
  });

  it("serves SSE frames on /svr/job/stream and rejects invalid job IDs", async () => {
    const bad = await fetch(`${apiUrl}/job/stream?id=0`);
    expect(bad.status).toBe(400);
    expect(await bad.json()).toMatchObject({
      code: 500,
      msg: "缺少必要参数",
    });

    const controller = new AbortController();
    const res = await fetch(`${apiUrl}/job/stream?id=1`, {
      signal: controller.signal,
    });
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toContain("text/event-stream");

    const reader = res.body!.getReader();
    const { value } = await reader.read();
    const chunk = new TextDecoder().decode(value);
    expect(chunk.startsWith("data: ")).toBe(true);
    const frame = JSON.parse(chunk.replace(/^data:\s*/, "").trim()) as {
      code: number;
      data: { taskId: number } | null;
      msg: string;
    };
    expect(frame.code).toBe(200);
    expect(frame.msg).toBe("success");
    expect(frame.data?.taskId).toBeGreaterThan(0);

    controller.abort();
    await reader.cancel().catch(() => {});
  });

  it("aligns mirror approval pathCount to 2 matching paths.length", () => {
    const store = createMockStore({
      jobs: [{ id: 10, remark: "镜像", method: 1, enable: 1 }],
    });
    const task = store.runJob(10);
    expect(task.approval).toBeUndefined();
    const approval = store.getPendingApproval(10);
    expect(approval).not.toBeNull();
    expect(approval?.pathCount).toBe(2);
    expect(approval?.paths).toHaveLength(2);
  });
});
