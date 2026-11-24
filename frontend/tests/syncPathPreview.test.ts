import { describe, expect, it } from "vitest";
import {
  buildSyncPathPreview,
  cleanPathList,
  commonSrcSelectionParent,
  dstPathForSrcSelection,
  jobDstMapKey,
  syncPathContains,
} from "../src/lib/syncPathPreview";
import {
  defaultJobForm,
  jobToForm,
  validateJobForm,
} from "../src/lib/taskForm";

describe("syncPathPreview parity with backend path mapping", () => {
  it("guides the user when source or destination paths are not yet selected", () => {
    expect(buildSyncPathPreview({ srcPaths: [], dstPaths: [] })).toMatchObject({
      rule: "empty",
      items: [],
      conflicts: [],
    });
    expect(
      buildSyncPathPreview({ srcPaths: ["/Photos"], dstPaths: [] }),
    ).toMatchObject({
      rule: "missing-dst",
      items: [],
      conflicts: [],
    });
    expect(
      buildSyncPathPreview({ srcPaths: [], dstPaths: ["/Backup"] }),
    ).toMatchObject({
      rule: "missing-src",
      items: [],
      conflicts: [],
    });
  });

  it("maps a single source directly onto the destination root without adding a subfolder", () => {
    const preview = buildSyncPathPreview({
      srcPaths: ["/media/photos"],
      dstPaths: ["/backup"],
    });
    expect(preview.rule).toBe("single-src");
    expect(preview.conflicts).toEqual([]);
    expect(preview.items).toEqual([
      expect.objectContaining({
        srcDir: "/media/photos/",
        dstBaseDir: "/backup/",
        dstBasePrefix: "/backup/",
        appendedSuffix: "",
        resolvedDst: "/backup/",
        preservedFromStoredMap: false,
        hasConflict: false,
      }),
    ]);
  });

  it("strips the deepest shared parent for sibling, nested, mixed, and same-leaf selections", () => {
    expect(commonSrcSelectionParent(["/drive/2", "/drive/学习"])).toBe("/drive");
    expect(
      commonSrcSelectionParent(["/drive/work/2", "/drive/study/notes"]),
    ).toBe("/drive");
    expect(commonSrcSelectionParent(["/nas/temp", "/nas/common/sec"])).toBe(
      "/nas",
    );
    expect(
      commonSrcSelectionParent(["/media/photos", "/archive/videos"]),
    ).toBe("/");

    const siblings = buildSyncPathPreview({
      srcPaths: ["/drive/2", "/drive/学习"],
      dstPaths: ["/backup"],
    });
    expect(siblings.rule).toBe("shared-parent");
    expect(siblings.commonParent).toBe("/drive");
    expect(siblings.items.map((item) => item.resolvedDst)).toEqual([
      "/backup/2/",
      "/backup/学习/",
    ]);
    expect(siblings.items.map((item) => item.appendedSuffix)).toEqual([
      "2/",
      "学习/",
    ]);

    const mixed = buildSyncPathPreview({
      srcPaths: [
        "/nas/docker",
        "/nas/photos",
        "/nas/temp",
        "/nas/common/sec",
        "/nas/common/sync",
      ],
      dstPaths: ["/google"],
    });
    expect(mixed.rule).toBe("shared-parent");
    expect(mixed.items.map((item) => item.resolvedDst)).toEqual([
      "/google/docker/",
      "/google/photos/",
      "/google/temp/",
      "/google/common/sec/",
      "/google/common/sync/",
    ]);

    const sameLeafUnderSharedParent = buildSyncPathPreview({
      srcPaths: ["/drive/work/2", "/drive/study/2"],
      dstPaths: ["/backup"],
    });
    expect(
      sameLeafUnderSharedParent.items.map((item) => item.resolvedDst),
    ).toEqual(["/backup/work/2/", "/backup/study/2/"]);
  });

  it("uses leaf base names when there is no shared parent and disambiguates collisions", () => {
    const distinct = buildSyncPathPreview({
      srcPaths: ["/media/photos", "/archive/videos"],
      dstPaths: ["/backup"],
    });
    expect(distinct.rule).toBe("base-name");
    expect(distinct.items.map((item) => item.resolvedDst)).toEqual([
      "/backup/photos/",
      "/backup/videos/",
    ]);

    const colliding = buildSyncPathPreview({
      srcPaths: ["/a/docs", "/b/docs", "/media/photos"],
      dstPaths: ["/google"],
    });
    expect(colliding.rule).toBe("disambiguated-base-name");
    expect(colliding.conflicts).toEqual([]);
    expect(colliding.items.map((item) => item.resolvedDst)).toEqual([
      "/google/a/docs/",
      "/google/b/docs/",
      "/google/photos/",
    ]);
    expect(colliding.items.map((item) => item.appendedSuffix)).toEqual([
      "a/docs/",
      "b/docs/",
      "photos/",
    ]);
  });

  it("groups items per destination when multiple destinations are selected", () => {
    const preview = buildSyncPathPreview({
      srcPaths: ["/media/photos", "/archive/videos"],
      dstPaths: ["/b1", "/b2/sub"],
    });
    expect(preview.groups).toHaveLength(2);
    expect(preview.groups[0].dstBaseDir).toBe("/b1/");
    expect(preview.groups[0].items.map((item) => item.resolvedDst)).toEqual([
      "/b1/photos/",
      "/b1/videos/",
    ]);
    expect(preview.groups[1].dstBaseDir).toBe("/b2/sub/");
    expect(preview.groups[1].items.map((item) => item.resolvedDst)).toEqual([
      "/b2/sub/photos/",
      "/b2/sub/videos/",
    ]);
  });

  it("honors persisted dstMap when editing sources without changing destinations", () => {
    const storedDstMap = JSON.stringify({
      version: 1,
      base: ["/backup/"],
      map: {
        [jobDstMapKey("/drive/a/x", "/backup")]: "/backup/x/",
        [jobDstMapKey("/drive/a/y", "/backup")]: "/backup/y/",
      },
    });

    // Adding /drive/b changes the fresh common parent to /drive, but existing
    // sources keep /backup/x/ and /backup/y/ while the new source gets /backup/b/.
    const added = buildSyncPathPreview({
      srcPaths: ["/drive/a/x", "/drive/a/y", "/drive/b"],
      dstPaths: ["/backup"],
      storedDstMap,
    });
    expect(added.hasPreservedMapping).toBe(true);
    expect(added.conflicts).toEqual([]);
    expect(added.items.map((item) => item.resolvedDst)).toEqual([
      "/backup/x/",
      "/backup/y/",
      "/backup/b/",
    ]);
    expect(added.items.map((item) => item.preservedFromStoredMap)).toEqual([
      true,
      true,
      false,
    ]);

    // Removing /drive/a/y still keeps /drive/a/x at /backup/x/.
    const removed = buildSyncPathPreview({
      srcPaths: ["/drive/a/x"],
      dstPaths: ["/backup"],
      storedDstMap,
    });
    expect(removed.hasPreservedMapping).toBe(true);
    expect(removed.items[0]).toMatchObject({
      resolvedDst: "/backup/x/",
      appendedSuffix: "x/",
      preservedFromStoredMap: true,
    });

    // Changing destination recomputes everything fresh.
    const changedDst = buildSyncPathPreview({
      srcPaths: ["/drive/a/x"],
      dstPaths: ["/backup2"],
      storedDstMap,
    });
    expect(changedDst.hasPreservedMapping).toBe(false);
    expect(changedDst.items[0]).toMatchObject({
      resolvedDst: "/backup2/",
      appendedSuffix: "",
      preservedFromStoredMap: false,
    });
  });

  it("ignores out-of-root stored dstMap entries", () => {
    const poisoned = JSON.stringify({
      version: 1,
      base: ["/backup/"],
      map: {
        [jobDstMapKey("/drive/a/x", "/backup")]: "/etc/",
      },
    });
    const preview = buildSyncPathPreview({
      srcPaths: ["/drive/a/x", "/drive/a/y"],
      dstPaths: ["/backup"],
      storedDstMap: poisoned,
    });
    expect(preview.items.map((item) => item.resolvedDst)).toEqual([
      "/backup/x/",
      "/backup/y/",
    ]);
    expect(preview.hasPreservedMapping).toBe(false);
  });

  it("detects case-insensitive overlap, nested sources, derived destination nesting, and move-mode conflicts", () => {
    const caseOverlap = buildSyncPathPreview({
      srcPaths: ["/Photos"],
      dstPaths: ["/photos"],
    });
    expect(caseOverlap.conflicts[0]).toContain("源目录与目标目录");
    expect(caseOverlap.items[0].hasConflict).toBe(true);

    // Three sources without shared parent: /x/a -> /D/x/a/, /y/a -> /D/y/a/, /z/x -> /D/x/
    // /D/x/ contains /D/x/a/!
    const derivedNested = buildSyncPathPreview({
      srcPaths: ["/x/a", "/y/a", "/z/x"],
      dstPaths: ["/D"],
    });
    expect(derivedNested.items.map((item) => item.resolvedDst)).toEqual([
      "/D/x/a/",
      "/D/y/a/",
      "/D/x/",
    ]);
    expect(derivedNested.conflicts[0]).toContain("多个目标目录");
    expect(derivedNested.items.map((item) => item.hasConflict)).toEqual([
      true,
      false,
      true,
    ]);

    // Existing single-source job mapped to /backup/ plus a newly added second source
    // conflicts because /backup/ contains /backup/Docs/.
    const singleJob = {
      id: 10,
      enable: 1,
      remark: "原单源任务",
      alistId: 1,
      srcPath: '["/Photos"]',
      dstPath: '["/Backup"]',
      dstMap: JSON.stringify({
        version: 1,
        base: ["/Backup/"],
        map: { [jobDstMapKey("/Photos", "/Backup")]: "/Backup/" },
      }),
      method: 0,
      isCron: 2,
      interval: 1440,
      useCacheS: 0,
      useCacheT: 0,
    };
    const editedForm = {
      ...jobToForm(singleJob),
      srcPath: ["/Photos", "/Docs"],
    };
    expect(validateJobForm(editedForm)).toContain("多个目标目录");

    // Case-insensitive overlap and derived destination nesting also rejected by validateJobForm.
    const base = { ...defaultJobForm(1), remark: "校验" };
    expect(
      validateJobForm({ ...base, srcPath: ["/Photos"], dstPath: ["/photos"] }),
    ).toContain("源目录与目标目录");
    expect(
      validateJobForm({
        ...base,
        srcPath: ["/x/a", "/y/a", "/z/x"],
        dstPath: ["/D"],
      }),
    ).toContain("多个目标目录");

    const moveMultiDst = buildSyncPathPreview({
      srcPaths: ["/Photos"],
      dstPaths: ["/B1", "/B2"],
      method: 2,
    });
    expect(moveMultiDst.conflicts).toContain("移动模式只允许一个目标目录");
  });

  it("matches backend edge cases for path cleaning, deduplication, and containment", () => {
    expect(cleanPathList(["/nas/a", "/nas/a/", " /nas/a "])).toEqual(["/nas/a"]);
    expect(dstPathForSrcSelection("/google", "/nas", ["/nas", "/nas/photos"])).toBe(
      "/google/",
    );
    expect(
      dstPathForSrcSelection("/google", "/nas/photos", ["/nas", "/nas/photos"]),
    ).toBe("/google/photos/");
    expect(syncPathContains("", "")).toBe(false);
    expect(syncPathContains("/", ".")).toBe(false);
    expect(syncPathContains("/", "relative")).toBe(false);
    expect(syncPathContains("/a/../b", "/b")).toBe(false);
    expect(syncPathContains("/d/", "/d")).toBe(true);
    expect(syncPathContains("/d", "/dx")).toBe(false);
    expect(syncPathContains("/", "/data")).toBe(true);
  });

  it("detects cross-job destination conflicts on the same engine", () => {
    const existingJobs = [
      {
        id: 1,
        remark: "已有相册任务",
        alistId: 7,
        srcPath: '["/Photos"]',
        dstPath: '["/Backup/Photos"]',
      },
      {
        id: 2,
        remark: "其他引擎任务",
        alistId: 8,
        srcPath: '["/Docs"]',
        dstPath: '["/Backup/Photos"]',
      },
    ];

    const conflicting = buildSyncPathPreview({
      srcPaths: ["/Camera"],
      dstPaths: ["/Backup/Photos"],
      existingJobs,
      engineId: 7,
    });
    expect(conflicting.conflicts[0]).toContain("已有相册任务");
    expect(conflicting.items[0].hasConflict).toBe(true);

    // Editing the same job does not conflict with itself
    const editingSelf = buildSyncPathPreview({
      srcPaths: ["/Photos"],
      dstPaths: ["/Backup/Photos"],
      existingJobs,
      currentJobId: 1,
      engineId: 7,
    });
    expect(editingSelf.conflicts).toEqual([]);

    // Same destination on a different engine does not conflict
    const differentEngine = buildSyncPathPreview({
      srcPaths: ["/Camera"],
      dstPaths: ["/Backup/Photos"],
      existingJobs,
      engineId: 9,
    });
    expect(differentEngine.conflicts).toEqual([]);
  });
});
