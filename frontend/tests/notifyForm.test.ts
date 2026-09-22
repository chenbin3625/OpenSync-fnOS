import { describe, expect, it } from "vitest";
import {
  buildNotifyParams,
  notifyDeliveryState,
  notifySendStatus,
  notifyToForm,
  validateNotifyForm,
} from "../src/lib/notifyForm";

describe("notification compatibility", () => {
  it("only submits fields belonging to the current channel", () => {
    expect(
      buildNotifyParams({
        method: 1,
        enable: true,
        sendKey: "secret",
        version: "v3",
        url: "https://wrong",
        corpsecret: "wrong",
        notSendNull: true,
      }),
    ).toEqual({ sendKey: "secret", version: "v3", notSendNull: true });
  });
  it("keeps backend string templates and object headers", () => {
    expect(
      buildNotifyParams({
        method: 0,
        enable: true,
        url: "https://example.com",
        httpMethod: "PUT",
        body: '{"text":"{content}"}',
        headers: '{"X-Key":"secret"}',
      }),
    ).toMatchObject({
      body: '{"text":"{content}"}',
      headers: { "X-Key": "secret" },
      httpMethod: "PUT",
    });
  });
  it("rejects webhook methods that the backend does not support", () => {
    const form = notifyToForm({
      id: 2,
      method: 0,
      enable: 1,
      params:
        '{"method":"PATCH","contentType":"application/x-www-form-urlencoded","needContent":false,"titleName":"subject","contentName":"message","url":"https://example.com"}',
    });
    expect(validateNotifyForm(form)).toContain("HTTP 方法");
  });
  it("restores legacy corporate parameter aliases and webhook JSON objects", () => {
    expect(
      notifyToForm({
        id: 1,
        enable: 1,
        method: 3,
        params: '{"corpId":"a","corpSecret":"b","agentId":"c","toUser":"d"}',
      }),
    ).toMatchObject({
      corpid: "a",
      corpsecret: "b",
      agentid: "c",
      touser: "d",
    });
    expect(
      notifyToForm({
        id: 2,
        enable: 1,
        method: 0,
        params: '{"body":{"text":"x"}}',
      }).body,
    ).toBe('{\n  "text": "x"\n}');
  });
  it("validates required fields even for test sends", () => {
    expect(validateNotifyForm({ method: 1, enable: true })).toContain(
      "SendKey",
    );
    expect(
      validateNotifyForm({
        method: 0,
        enable: true,
        url: "javascript:alert(1)",
      }),
    ).toContain("URL");
  });
  it("passes the untouched redaction marker straight through", () => {
    // The backend replaces a redacted headers value with exactly "****" and a
    // body template with exactly "******"; both must round-trip unchanged so
    // saving an unedited form keeps the stored credentials.
    expect(
      buildNotifyParams({
        method: 0,
        enable: true,
        url: "https://example.com",
        headers: "****",
        body: "******",
      }),
    ).toMatchObject({ headers: "****", body: "******" });
    expect(
      validateNotifyForm({
        method: 0,
        enable: true,
        url: "https://example.com",
        headers: "****",
        body: "******",
      }),
    ).toBe("");
  });
  it("rejects malformed JSON that merely contains the marker", () => {
    // `includes("****")` used to short-circuit here, so this was submitted as a
    // bare string and silently discarded by the backend instead of reported.
    expect(
      validateNotifyForm({
        method: 0,
        enable: true,
        url: "https://example.com",
        headers: '{"Authorization":"****"',
      }),
    ).toContain("JSON");
    expect(() =>
      buildNotifyParams({
        method: 0,
        enable: true,
        url: "https://example.com",
        headers: '{"Authorization":"****"',
      }),
    ).toThrow();
  });
  it("still rejects a JSON array of headers that contains the marker", () => {
    expect(
      validateNotifyForm({
        method: 0,
        enable: true,
        url: "https://example.com",
        headers: '["****"]',
      }),
    ).toContain("JSON");
  });
});

describe("notification delivery state", () => {
  it("reports a failure with its reason", () => {
    expect(
      notifyDeliveryState({
        lastSendStatus: notifySendStatus.failed,
        lastSendError: "notify request failed: 403 Forbidden",
      }),
    ).toEqual({
      tone: "error",
      label: "最近一次发送失败",
      reason: "notify request failed: 403 Forbidden",
    });
  });
  it("reports a failure without a stored reason", () => {
    expect(
      notifyDeliveryState({
        lastSendStatus: notifySendStatus.failed,
        lastSendError: "   ",
      }),
    ).toEqual({ tone: "error", label: "最近一次发送失败" });
  });
  it("reports a success as muted meta text", () => {
    expect(
      notifyDeliveryState({ lastSendStatus: notifySendStatus.success }),
    ).toEqual({ tone: "muted", label: "最近一次发送成功" });
  });
  it("shows nothing for a config that was never used", () => {
    // 新建渠道没有投递记录，显示"失败"或"成功"都是假信息。
    expect(notifyDeliveryState({})).toBeNull();
    expect(
      notifyDeliveryState({ lastSendStatus: notifySendStatus.unknown }),
    ).toBeNull();
  });
});
