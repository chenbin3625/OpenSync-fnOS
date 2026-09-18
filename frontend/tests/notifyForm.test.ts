import { describe, expect, it } from "vitest";
import {
  buildNotifyParams,
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
  it("preserves all existing custom webhook controls and legacy HTTP method", () => {
    const form = notifyToForm({
      id: 2,
      method: 0,
      enable: 1,
      params:
        '{"method":"PATCH","contentType":"application/x-www-form-urlencoded","needContent":false,"titleName":"subject","contentName":"message","url":"https://example.com"}',
    });
    expect(buildNotifyParams(form)).toMatchObject({
      httpMethod: "PATCH",
      contentType: "application/x-www-form-urlencoded",
      needContent: false,
      titleName: "subject",
      contentName: "message",
    });
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
});
