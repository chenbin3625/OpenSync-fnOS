import { describe, expect, it } from "vitest";
import {
  buildSyncPathPreview,
  cleanPathList,
  commonSrcSelectionParent,
  dstPathForSrcSelection,
  formatDisplayPath,
  formatDisplayPathList,
  jobDstMapKey,
  stripNativeLocalRootID,
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

  it("strips native local rootID fingerprints from derived multi-source destinations unless colliding", () => {
    expect(stripNativeLocalRootID("docker~b3fbe6ed1ca9")).toEqual({
      base: "docker",
      stripped: true,
    });
    expect(stripNativeLocalRootID("my~dir~0123456789ab")).toEqual({
      base: "my~dir",
      stripped: true,
    });
    expect(stripNativeLocalRootID("..~0123456789ab")).toEqual({
      base: "..~0123456789ab",
      stripped: false,
    });
    expect(stripNativeLocalRootID("docker~B3FBE6ED1CA9")).toEqual({
      base: "docker~B3FBE6ED1CA9",
      stripped: false,
    });

    const multiRoots = buildSyncPathPreview({
      srcPaths: [
        "/本地/docker~b3fbe6ed1ca9",
        "/本地/X~35c37bec9b16",
        "/本地/photos~5f78544a37a3",
        "/本地/sec~972efbdc2b1f",
        "/本地/sync~bd3b4223a876",
      ],
      dstPaths: ["/openlist/quark"],
    });
    expect(multiRoots.rule).toBe("shared-parent");
    expect(multiRoots.commonParent).toBe("/本地");
    expect(multiRoots.conflicts).toEqual([]);
    expect(multiRoots.items.map((item) => item.resolvedDst)).toEqual([
      "/openlist/quark/docker/",
      "/openlist/quark/X/",
      "/openlist/quark/photos/",
      "/openlist/quark/sec/",
      "/openlist/quark/sync/",
    ]);
    expect(multiRoots.items.map((item) => item.appendedSuffix)).toEqual([
      "docker/",
      "X/",
      "photos/",
      "sec/",
      "sync/",
    ]);

    // Subdirectories under different local roots keep deeper names intact.
    const nestedRoots = buildSyncPathPreview({
      srcPaths: [
        "/本地/docker~b3fbe6ed1ca9/foo~0123456789ab",
        "/本地/photos~5f78544a37a3/2025",
      ],
      dstPaths: ["/openlist/quark"],
    });
    expect(nestedRoots.items.map((item) => item.resolvedDst)).toEqual([
      "/openlist/quark/docker/foo~0123456789ab/",
      "/openlist/quark/photos/2025/",
    ]);

    // Subdirectories under the same local root display a clean shared parent.
    const sameRootSubdirs = buildSyncPathPreview({
      srcPaths: [
        "/本地/docker~b3fbe6ed1ca9/nginx",
        "/本地/docker~b3fbe6ed1ca9/redis",
      ],
      dstPaths: ["/openlist/quark"],
    });
    expect(sameRootSubdirs.commonParent).toBe("/本地/docker");
    expect(sameRootSubdirs.items.map((item) => item.resolvedDst)).toEqual([
      "/openlist/quark/nginx/",
      "/openlist/quark/redis/",
    ]);

    // Two distinct volumes sharing a folder name (case-insensitively) retain
    // their fingerprints while non-colliding siblings still strip cleanly.
    const collidingRoots = buildSyncPathPreview({
      srcPaths: [
        "/本地/Photos~111111111111",
        "/本地/photos~222222222222",
        "/本地/sec~972efbdc2b1f",
      ],
      dstPaths: ["/openlist/quark"],
    });
    expect(collidingRoots.conflicts).toEqual([]);
    expect(collidingRoots.items.map((item) => item.resolvedDst)).toEqual([
      "/openlist/quark/Photos~111111111111/",
      "/openlist/quark/photos~222222222222/",
      "/openlist/quark/sec/",
    ]);

    // Root vs subdirectory of same-named roots on different volumes never nests.
    const rootVsSub = buildSyncPathPreview({
      srcPaths: [
        "/本地/docker~111111111111",
        "/本地/docker~222222222222/sub",
      ],
      dstPaths: ["/openlist/quark"],
    });
    expect(rootVsSub.conflicts).toEqual([]);
    expect(rootVsSub.items.map((item) => item.resolvedDst)).toEqual([
      "/openlist/quark/docker~111111111111/",
      "/openlist/quark/docker~222222222222/sub/",
    ]);

    // Local-to-local backup preserves the target local rootID while cleaning source suffixes.
    const localToLocal = buildSyncPathPreview({
      srcPaths: [
        "/本地/docker~b3fbe6ed1ca9",
        "/本地/photos~5f78544a37a3",
      ],
      dstPaths: ["/本地/backup~112233445566"],
    });
    expect(localToLocal.conflicts).toEqual([]);
    expect(localToLocal.items.map((item) => item.resolvedDst)).toEqual([
      "/本地/backup~112233445566/docker/",
      "/本地/backup~112233445566/photos/",
    ]);

    // Cross-alias case-insensitive leaf collision disambiguates cleanly.
    const crossAlias = buildSyncPathPreview({
      srcPaths: [
        "/本地/docker~111111111111",
        "/disk/other~222222222222/Docker",
      ],
      dstPaths: ["/openlist/quark"],
    });
    expect(crossAlias.rule).toBe("disambiguated-base-name");
    expect(crossAlias.conflicts).toEqual([]);
    expect(crossAlias.items.map((item) => item.resolvedDst)).toEqual([
      "/openlist/quark/本地/docker/",
      "/openlist/quark/disk/other/Docker/",
    ]);

    // Existing saved jobs keep their legacy hashed destination via dstMap until dstPath changes.
    const legacyDstMap = JSON.stringify({
      version: 1,
      base: ["/openlist/quark/"],
      map: {
        [jobDstMapKey("/本地/docker~b3fbe6ed1ca9", "/openlist/quark")]:
          "/openlist/quark/docker~b3fbe6ed1ca9/",
      },
    });
    const preserved = buildSyncPathPreview({
      srcPaths: [
        "/本地/docker~b3fbe6ed1ca9",
        "/本地/sec~972efbdc2b1f",
      ],
      dstPaths: ["/openlist/quark"],
      storedDstMap: legacyDstMap,
    });
    expect(preserved.hasPreservedMapping).toBe(true);
    expect(preserved.items.map((item) => item.resolvedDst)).toEqual([
      "/openlist/quark/docker~b3fbe6ed1ca9/",
      "/openlist/quark/sec/",
    ]);
  });

  it("formats native local root paths for display without the fingerprint suffix unless colliding", () => {
    expect(formatDisplayPath("")).toBe("");
    expect(formatDisplayPath("/本地/docker~b3fbe6ed1ca9")).toBe("/本地/docker");
    expect(formatDisplayPath("/本地/docker~b3fbe6ed1ca9/")).toBe("/本地/docker/");
    expect(
      formatDisplayPath("/本地/docker~b3fbe6ed1ca9/foo~0123456789ab"),
    ).toBe("/本地/docker/foo~0123456789ab");
    expect(formatDisplayPath("/openlist/quark/docker~b3fbe6ed1ca9/")).toBe(
      "/openlist/quark/docker~b3fbe6ed1ca9/",
    );

    const nonColliding = [
      "/本地/docker~b3fbe6ed1ca9",
      "/本地/photos~5f78544a37a3",
      "/本地/sec~972efbdc2b1f",
      "/本地/sync~bd3b4223a876",
    ];
    expect(formatDisplayPathList(nonColliding)).toEqual([
      "/本地/docker",
      "/本地/photos",
      "/本地/sec",
      "/本地/sync",
    ]);

    // Sibling subdirectories under the same local root share one rootID and strip cleanly.
    expect(
      formatDisplayPathList([
        "/本地/docker~b3fbe6ed1ca9/nginx",
        "/本地/docker~b3fbe6ed1ca9/redis",
      ]),
    ).toEqual(["/本地/docker/nginx", "/本地/docker/redis"]);

    // Distinct volumes with the same folder name retain their suffixes so they stay distinguishable.
    expect(
      formatDisplayPathList([
        "/本地/Photos~111111111111",
        "/本地/photos~222222222222",
        "/本地/sec~972efbdc2b1f",
      ]),
    ).toEqual([
      "/本地/Photos~111111111111",
      "/本地/photos~222222222222",
      "/本地/sec",
    ]);
  });
});
