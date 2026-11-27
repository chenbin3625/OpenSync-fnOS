import { describe, expect, it } from "vitest";
import {
  buildJobPayload,
  defaultJobForm,
  jobToForm,
  validateJobFormFields,
} from "../src/lib/taskForm";
import {
  applyWizardEndpoints,
  canSwitchBackToWizard,
  extractFallbackLocalRoots,
  extractWizardPaths,
  formatLocalRootDisplayItems,
  formatNasDisplayPath,
  inferOneWayDeleteMode,
  inferWizardDirection,
  isNativeLocalPath,
  isWizardCompatibleExistingJob,
  normalizeWizardFolderChildren,
} from "../src/lib/taskWizard";
import { applyWeeklyScheduleTemplate } from "../src/lib/weeklySchedule";
import type { JobItem } from "../src/types";

describe("taskWizard helpers", () => {
  it("hides /本地 and ~12hex fingerprint for unique local roots and disambiguates duplicate names", () => {
    const roots = [
      { id: "Photos~a1b2c3d4e5f6", path: "/vol1/1000/Photos" },
      { id: "Photos~fedcba987654", path: "/vol2/1000/Photos" },
      { id: "Documents~112233445566", path: "/vol1/1000/Documents" },
    ];
    const items = formatLocalRootDisplayItems(roots);
    expect(items.find((i) => i.id === "Documents~112233445566")?.label).toBe(
      "Documents",
    );
    expect(items.find((i) => i.id === "Photos~a1b2c3d4e5f6")?.label).toBe(
      "Photos (vol1)",
    );
    expect(items.find((i) => i.id === "Photos~fedcba987654")?.label).toBe(
      "Photos (vol2)",
    );
    for (const item of items) {
      expect(item.label).not.toContain("/本地");
      expect(item.label).not.toMatch(/~[0-9a-f]{12}/i);
    }

    expect(
      formatNasDisplayPath("/本地/Documents~112233445566/2026/Work", roots),
    ).toBe("Documents/2026/Work");
    expect(
      formatNasDisplayPath("/本地/Photos~a1b2c3d4e5f6/Trips", roots),
    ).toBe("Photos (vol1)/Trips");
  });

  it("detects wizard compatibility cleanly (1:1 NAS <-> Cloud vs multi-path/cloud-cloud/legacy)", () => {
    const base = defaultJobForm(1);

    expect(
      isWizardCompatibleExistingJob({
        ...base,
        srcPath: ["/本地/Photos~a1b2c3d4e5f6"],
        dstPath: ["/aliyun/Backup"],
        method: 0,
        isCron: 3,
      }),
    ).toBe(true);

    expect(
      isWizardCompatibleExistingJob({
        ...base,
        srcPath: ["/aliyun/Backup"],
        dstPath: ["/本地/Photos~a1b2c3d4e5f6"],
        method: 3,
        isCron: 3,
      }),
    ).toBe(true);

    // Cloud-to-cloud -> Free Mode
    expect(
      isWizardCompatibleExistingJob({
        ...base,
        srcPath: ["/Photos"],
        dstPath: ["/Backup"],
        method: 0,
        isCron: 3,
      }),
    ).toBe(false);

    // Multi-destination -> Free Mode
    expect(
      isWizardCompatibleExistingJob({
        ...base,
        srcPath: ["/本地/Photos"],
        dstPath: ["/Cloud1/a", "/Cloud2/b"],
        method: 0,
        isCron: 3,
      }),
    ).toBe(false);

    // Local-to-local -> Free Mode
    expect(
      isWizardCompatibleExistingJob({
        ...base,
        srcPath: ["/本地/Photos"],
        dstPath: ["/本地/Backup"],
        method: 0,
        isCron: 3,
      }),
    ).toBe(false);

    // Legacy non-continuous or move mode -> Free Mode
    expect(
      isWizardCompatibleExistingJob({
        ...base,
        srcPath: ["/本地/Photos"],
        dstPath: ["/aliyun/Backup"],
        method: 2,
        isCron: 3,
      }),
    ).toBe(false);
    expect(
      isWizardCompatibleExistingJob({
        ...base,
        srcPath: ["/本地/Photos"],
        dstPath: ["/aliyun/Backup"],
        method: 0,
        isCron: 1,
      }),
    ).toBe(false);

    expect(canSwitchBackToWizard({ ...base, srcPath: [], dstPath: [] })).toBe(
      true,
    );
    expect(
      canSwitchBackToWizard({
        ...base,
        srcPath: ["/CloudA"],
        dstPath: ["/CloudB"],
      }),
    ).toBe(false);
  });

  it("derives endpoints for upload, download, bidirectional and delete modes", () => {
    const nas = "/本地/Photos~a1b2c3d4e5f6";
    const remote = "/aliyun/Backup";

    expect(applyWizardEndpoints("upload", "none", nas, remote)).toEqual({
      srcPath: [nas],
      dstPath: [remote],
      method: 0,
    });
    expect(applyWizardEndpoints("upload", "tracked", nas, remote)).toEqual({
      srcPath: [nas],
      dstPath: [remote],
      method: 0,
    });
    expect(applyWizardEndpoints("upload", "mirror", nas, remote)).toEqual({
      srcPath: [nas],
      dstPath: [remote],
      method: 1,
    });
    expect(applyWizardEndpoints("download", "none", nas, remote)).toEqual({
      srcPath: [remote],
      dstPath: [nas],
      method: 0,
    });
    expect(applyWizardEndpoints("download", "mirror", nas, remote)).toEqual({
      srcPath: [remote],
      dstPath: [nas],
      method: 1,
    });
    expect(applyWizardEndpoints("bidirectional", "mirror", nas, remote)).toEqual({
      srcPath: [nas],
      dstPath: [remote],
      method: 3,
    });

    expect(
      extractWizardPaths({ srcPath: [remote], dstPath: [nas] }),
    ).toEqual({ nasPath: nas, remotePath: remote });
    expect(isNativeLocalPath(nas)).toBe(true);
    expect(isNativeLocalPath(remote)).toBe(false);
  });

  it("round-trips syncDirectionMode, oneWayDeleteMode and weeklySchedule through buildJobPayload and jobToForm", () => {
    const ws = applyWeeklyScheduleTemplate("workday_limited_night_full");
    ws.limitRate = "12M";
    ws.stopOutside = true;

    const form = {
      ...defaultJobForm(1),
      remark: "加密同步",
      srcPath: ["/本地/Photos~a1b2c3d4e5f6"],
      dstPath: ["/aliyun/Backup"],
      method: 0,
      isCron: 3,
      trashEnable: true,
      extraFlags: {
        ...defaultJobForm(1).extraFlags,
        syncDirectionMode: "upload" as const,
        oneWayDeleteMode: "tracked" as const,
        weeklySchedule: ws,
      },
    };

    const payload = buildJobPayload(form);
    const parsedExtra = JSON.parse(String(payload.extraFlags));
    expect(parsedExtra.syncDirectionMode).toBe("upload");
    expect(parsedExtra.oneWayDeleteMode).toBe("tracked");
    expect(parsedExtra.weeklySchedule).toEqual(ws);

    const restored = jobToForm({
      ...(payload as unknown as JobItem),
      id: 11,
      srcPath: JSON.stringify(payload.srcPath),
      dstPath: JSON.stringify(payload.dstPath),
    });
    expect(inferWizardDirection(restored)).toBe("upload");
    expect(inferOneWayDeleteMode(restored)).toBe("tracked");
    expect(restored.extraFlags.weeklySchedule).toEqual(ws);

    // Disabled weeklySchedule is omitted from extraFlags on save
    const withoutSchedule = buildJobPayload({
      ...restored,
      extraFlags: {
        ...restored.extraFlags,
        weeklySchedule: { ...ws, enabled: false },
      },
    });
    expect(JSON.parse(String(withoutSchedule.extraFlags))).not.toHaveProperty(
      "weeklySchedule",
    );
  });

  it("validates weeklySchedule and routes wizard step errors accurately", () => {
    const ws = applyWeeklyScheduleTemplate("workday_limited_night_full");
    ws.limitRate = "";
    const invalidForm = {
      ...defaultJobForm(1),
      remark: "测试任务",
      srcPath: ["/本地/Photos"],
      dstPath: ["/aliyun/Backup"],
      extraFlags: {
        ...defaultJobForm(1).extraFlags,
        weeklySchedule: ws,
      },
    };

    const wizardCheck = validateJobFormFields(invalidForm, { mode: "wizard" });
    expect(wizardCheck.fields.weeklySchedule).toContain("限速速率");
    expect(wizardCheck.stepErrors[0]).toBe("");
    expect(wizardCheck.stepErrors[1]).toBe("");
    expect(wizardCheck.stepErrors[2]).toContain("限速速率");

    // In wizard mode, invalid trashDir belongs to Step 1 (safety step), not Step 0
    const badTrash = {
      ...defaultJobForm(1),
      remark: "回收站检查",
      srcPath: ["/本地/Photos"],
      dstPath: ["/aliyun/Backup"],
      trashEnable: true,
      trashDir: "/invalid/abs",
    };
    const trashWizard = validateJobFormFields(badTrash, { mode: "wizard" });
    expect(trashWizard.stepErrors[0]).toBe("");
    expect(trashWizard.stepErrors[1]).toContain("相对路径");
  });

  it("normalizes absolute backend folder paths without duplicating parent prefixes (issue #18)", () => {
    // Local NAS subfolder: backend returns { name: "rime", path: "/本地/sync~92618e31210e/rime" }
    expect(
      normalizeWizardFolderChildren("/本地/sync~92618e31210e", [
        { name: "rime", path: "/本地/sync~92618e31210e/rime" },
      ]),
    ).toEqual([
      { name: "rime", fullPath: "/本地/sync~92618e31210e/rime" },
    ]);

    // Deeper nested local subfolder
    expect(
      normalizeWizardFolderChildren("/本地/sync~92618e31210e/rime", [
        { name: "build", path: "/本地/sync~92618e31210e/rime/build" },
      ]),
    ).toEqual([
      { name: "build", fullPath: "/本地/sync~92618e31210e/rime/build" },
    ]);

    // Cloud remote subfolder: backend returns { name: "sync", path: "/jianguoyun/sync" }
    expect(
      normalizeWizardFolderChildren("/jianguoyun", [
        { name: "sync", path: "/jianguoyun/sync" },
      ]),
    ).toEqual([{ name: "sync", fullPath: "/jianguoyun/sync" }]);

    // Fallback when backend or mock only provides absolute path without name
    expect(
      normalizeWizardFolderChildren("/jianguoyun/sync", [
        { path: "/jianguoyun/sync/notes" },
      ]),
    ).toEqual([{ name: "notes", fullPath: "/jianguoyun/sync/notes" }]);

    // Fallback when entry only has relative name
    expect(
      normalizeWizardFolderChildren("/jianguoyun", [
        { name: "docs" },
        { name: "." },
        { name: ".." },
      ]),
    ).toEqual([{ name: "docs", fullPath: "/jianguoyun/docs" }]);

    // Fallback `/本地` root listing extracts root ID and host path
    const localEntries = normalizeWizardFolderChildren("/本地", [
      { name: "/vol1/1000/主文件夹/sync", path: "/本地/sync~92618e31210e" },
    ]);
    expect(extractFallbackLocalRoots(localEntries)).toEqual([
      { id: "sync~92618e31210e", path: "/vol1/1000/主文件夹/sync" },
    ]);
  });
});
