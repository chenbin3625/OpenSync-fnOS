import { describe, expect, it } from "vitest";
import {
  buildJobPayload,
  buildTaskSearchParams,
  defaultJobForm,
  fileTypeFilterGroups,
  findBatchMoveJobs,
  isExcludePatternEnabled,
  jobToForm,
  normalizeCustomFileTypePattern,
  parseExcludeFolders,
  parseTaskTab,
  resolveJobSelection,
  resolveTaskTab,
  selectJob,
  systemDirFilterGroups,
  updateExcludePatterns,
  updateExcludeFolders,
  validateJobForm,
} from "../src/lib/taskForm";
import { methodNames, methodOptions } from "../src/pages/Home/homeUtils";
import {
  initialExcludeExpandedKeys,
  normalizeExcludeRootPath,
} from "../src/lib/excludeTree";
import * as taskFormModule from "../src/lib/taskForm";

describe("existing synchronization contracts", () => {
  it("keeps copy modes and exposes verified single-target moves and bisync", () => {
    expect(methodNames).toEqual(["增量同步", "全量同步", "移动", "双向同步"]);
    expect(methodOptions.map((method) => method.description).join("\n")).toContain(
      "不会删除目标端多余文件",
    );
    expect(methodOptions.map((method) => method.description).join("\n")).toContain(
      "会删除目标端多余文件",
    );
    expect(methodOptions.map((method) => method.description).join("\n")).toContain(
      "首次运行建立同步基线",
    );

    for (const method of [0, 1]) {
      const form = {
        ...defaultJobForm(7),
        method,
        srcPath: ["/Photos", "/Docs"],
        dstPath: ["/Backup", "/Archive"],
      };
      expect(buildJobPayload(form)).toMatchObject({
        alistId: 7,
        method,
        srcPath: form.srcPath,
        dstPath: form.dstPath,
      });
    }
  });

  it("rejects move mode in continuous sync while validating one-way and bidirectional targets", () => {
    const form = {
      ...defaultJobForm(7),
      remark: "移动",
      method: 2,
      srcPath: ["/Source"],
      dstPath: ["/Backup", "/Archive"],
    };
    expect(validateJobForm(form)).toContain("一个目标");
    expect(validateJobForm({ ...form, dstPath: ["/Backup"] })).toContain("持续同步");
  });

  it("expands selected source roots by default for folder filtering", () => {
    expect(initialExcludeExpandedKeys(["/Photos", "/Docs/"])).toEqual([
      "/Photos",
      "/Docs",
    ]);
    expect(normalizeExcludeRootPath("///")).toBe("/");
  });
  it("persists paused continuous sync state and translates file size units", () => {
    const payload = buildJobPayload({
      ...defaultJobForm(7),
      enable: false,
      minFileSize: 2,
      minFileSizeUnit: "GB",
    });
    expect(payload.isCron).toBe(3);
    expect(payload.enable).toBe(0);
    expect(payload.minFileSize).toBe(2 * 1024 ** 3);
  });
  it("excludes Synology recycle directories by default", () => {
    expect(defaultJobForm().exclude.split("\n")).toContain("#recycle/");
  });
  it("enables safe default file and folder filters", () => {
    const exclude = defaultJobForm().exclude;
    expect(parseExcludeFolders(exclude)).toEqual([]);
    const enabledFileGroups = new Set(["system", "temporary", "lock"]);
    for (const group of fileTypeFilterGroups) {
      const selected = group.patterns.filter((pattern) =>
        isExcludePatternEnabled(exclude, pattern),
      );
      if (enabledFileGroups.has(group.key)) {
        expect(selected, group.label).toEqual([...group.patterns]);
      } else {
        expect(selected, group.label).toEqual([]);
      }
    }

    for (const group of systemDirFilterGroups) {
      expect(
        group.patterns.filter((pattern) =>
          isExcludePatternEnabled(exclude, pattern),
        ),
        group.label,
      ).toEqual([...group.patterns]);
    }
  });
  it("toggles file type presets without changing unrelated exclude rules", () => {
    const filters = taskFormModule as typeof taskFormModule & {
      updateExcludePatterns?: (
        exclude: string,
        patterns: string[],
        enabled: boolean,
      ) => string;
    };
    expect(filters.updateExcludePatterns).toBeTypeOf("function");

    const original = "keep-this/\n*.tmp\n# *.log\nphotos/raw/";
    const disabled = filters.updateExcludePatterns!(
      original,
      ["*.tmp", "*.temp"],
      false,
    );
    // No "# " comment: the backend drops comments on save, so a disabled rule
    // must be removed instead of being parked as a comment.
    expect(disabled).not.toContain("*.tmp");
    expect(disabled).toContain("# *.log");
    expect(disabled).toContain("keep-this/");
    expect(disabled).toContain("photos/raw/");

    const enabled = filters.updateExcludePatterns!(
      disabled,
      ["*.tmp", "*.temp"],
      true,
    );
    expect(enabled.split("\n")).toEqual(
      expect.arrayContaining(["*.tmp", "*.temp"]),
    );
  });
  it("can disable and re-enable a directory rule whose name starts with #", () => {
    const disabled = taskFormModule.updateExcludePatterns(
      "#recycle/",
      ["#recycle/"],
      false,
    );
    expect(disabled).toBe("");
    expect(isExcludePatternEnabled(disabled, "#recycle/")).toBe(false);
    expect(
      taskFormModule.updateExcludePatterns(
        disabled,
        ["#recycle/"],
        true,
      ),
    ).toBe("#recycle/");
  });
  it("adds custom file names and wildcard extension patterns separately", () => {
    const filters = taskFormModule as typeof taskFormModule & {
      addCustomFileTypeFilter?: (exclude: string, input: string) => string;
      parseCustomFileTypeFilters?: (
        exclude: string,
      ) => { pattern: string; enabled: boolean }[];
      parseOtherFileTypeFilters?: (
        exclude: string,
      ) => { pattern: string; enabled: boolean }[];
      updateExcludePatterns?: (
        exclude: string,
        patterns: string[],
        enabled: boolean,
      ) => string;
    };
    expect(filters.addCustomFileTypeFilter).toBeTypeOf("function");
    expect(filters.parseCustomFileTypeFilters).toBeTypeOf("function");
    expect(filters.parseOtherFileTypeFilters).toBeTypeOf("function");

    let added = filters.addCustomFileTypeFilter!(".DS_Store", "a.bcd");
    added = filters.addCustomFileTypeFilter!(added, "ipc-socket");
    added = filters.addCustomFileTypeFilter!(added, "*.DOC");
    expect(filters.parseOtherFileTypeFilters!(added)).toEqual([
      { pattern: "a.bcd", enabled: true },
      { pattern: "ipc-socket", enabled: true },
    ]);
    expect(isExcludePatternEnabled(added, "*.doc")).toBe(true);
    const disabled = filters.updateExcludePatterns!(added, ["a.bcd"], false);
    // "禁用" == 删除：后端 normalizeExclude 会丢弃 "# " 注释，保留注释形式的
    // 禁用规则只会在保存后静默消失。
    expect(filters.parseOtherFileTypeFilters!(disabled)).toEqual([
      { pattern: "ipc-socket", enabled: true },
    ]);
    expect(disabled).not.toContain("a.bcd");
    expect(disabled).not.toContain("# a.bcd");
    expect(isExcludePatternEnabled(disabled, "*.doc")).toBe(true);
    expect(disabled).toContain(".DS_Store");
  });
  it("accepts only supported file name and extension rules", () => {
    const filters = taskFormModule as typeof taskFormModule & {
      addCustomFileTypeFilter?: (exclude: string, input: string) => string;
      parseOtherFileTypeFilters?: (
        exclude: string,
      ) => { pattern: string; enabled: boolean }[];
    };
    expect(filters.addCustomFileTypeFilter).toBeTypeOf("function");

    expect(filters.addCustomFileTypeFilter!("", "!important.txt")).toBe("");
    expect(filters.addCustomFileTypeFilter!("", "report[1].txt")).toBe("");
    expect(filters.addCustomFileTypeFilter!("", "folder/report.txt")).toBe("");
    expect(filters.addCustomFileTypeFilter!("", "report+1.txt")).not.toBe("");

    const added = filters.addCustomFileTypeFilter!("", "*.PST");
    expect(filters.parseOtherFileTypeFilters!(added)).toEqual([
      { pattern: "*.pst", enabled: true },
    ]);
  });
  it("preserves the original extension spelling when toggling or removing it", () => {
    const filters = taskFormModule as typeof taskFormModule & {
      addCustomFileTypeFilter?: (exclude: string, input: string) => string;
      removeCustomFileTypeFilter?: (exclude: string, pattern: string) => string;
      updateExcludePatterns?: (
        exclude: string,
        patterns: string[],
        enabled: boolean,
      ) => string;
    };
    const added = filters.addCustomFileTypeFilter!("", "*.PST");
    expect(isExcludePatternEnabled(added, "*.pst")).toBe(true);
    const disabled = filters.updateExcludePatterns!(
      added,
      ["*.pst"],
      false,
    );

    // The live rule is removed; the marker comments (which the backend drops on
    // save anyway) are left alone.
    expect(disabled).not.toContain("*.PST");
    expect(filters.parseOtherFileTypeFilters!(disabled)).toEqual([]);
    expect(
      filters.removeCustomFileTypeFilter!(added, "*.pst"),
    ).toBe("");
  });
  it("treats legacy manual file patterns and file names as other file filters", () => {
    const filters = taskFormModule as typeof taskFormModule & {
      parseOtherFileTypeFilters?: (
        exclude: string,
      ) => { pattern: string; enabled: boolean }[];
    };
    expect(filters.parseOtherFileTypeFilters).toBeTypeOf("function");

    expect(
      filters.parseOtherFileTypeFilters!(
        "keep-dir/\nreport.final\n*.pst\n# *.vmdk\n*.tmp\n.DS_Store",
      ),
    ).toEqual([
      { pattern: "report.final", enabled: true },
      { pattern: "*.pst", enabled: true },
      { pattern: "*.vmdk", enabled: false },
    ]);
  });
  it("keeps the default filters focused on temporary files and system metadata", () => {
    const exclude = defaultJobForm().exclude;
    expect(exclude).toContain("*.partial");
    expect(exclude).not.toContain("*.bak");
    expect(exclude).not.toContain(".git/");
  });
  it("round trips existing job identifiers and normalizes schedules to continuous sync", () => {
    const job = {
      id: 8,
      alistId: 7,
      enable: 0,
      srcPath: '["/a","/b"]',
      dstPath: '["/c"]',
      remark: "test",
      method: 1,
      isCron: 3,
      interval: 33,
      useCacheS: 1,
      useCacheT: 0,
      scanIntervalS: 77,
      scanIntervalT: 88,
      minFileSize: 1024,
      maxFileSize: 4096,
      exclude: "*.tmp",
    };
    expect(buildJobPayload(jobToForm(job))).toMatchObject({
      ...job,
      isCron: 3,
      srcPath: ["/a", "/b"],
      dstPath: ["/c"],
    });
  });
  it("preserves a legacy move task when saving edits", () => {
    const job = {
      id: 9,
      alistId: 7,
      srcPath: '["/a"]',
      dstPath: '["/b"]',
      method: 2,
      enable: 1,
      useCacheS: 0,
      useCacheT: 0,
      interval: 0,
      isCron: 2,
    };
    const form = jobToForm(job);

    const edited = { ...form, remark: "改名" };
    expect(validateJobForm(edited)).toBe("");
    expect(buildJobPayload(edited)).toMatchObject({
      method: 2,
      isCron: 2,
      remark: "改名",
    });
  });

  it("rejects sources and destinations that are equal or nested", () => {
    const base = { ...defaultJobForm(7), remark: "路径校验" };
    const error = (srcPath: string[], dstPath: string[]) =>
      validateJobForm({ ...base, srcPath, dstPath });

    expect(error(["/Photos"], ["/Photos"])).toContain("源目录与目标目录");
    expect(error(["/Photos"], ["/Photos/Backup"])).toContain("源目录与目标目录");
    expect(error(["/Photos/Backup"], ["/Photos"])).toContain("源目录与目标目录");
    // 归一化后相同（尾斜杠/重复斜杠）也要能识别
    expect(error(["/Photos/"], ["//Photos"])).toContain("源目录与目标目录");
    expect(error(["/Photos", "/Photos/Raw"], ["/Backup"])).toContain(
      "多个源目录",
    );
    expect(error(["/Photos"], ["/Backup", "/Backup/Nightly"])).toContain(
      "多个目标目录",
    );
    expect(error(["/Photos"], ["/Backup"])).toBe("");
  });

  it("validates custom file filter input before trying to add it", () => {
    expect(normalizeCustomFileTypePattern("  ")).toBe("");
    expect(normalizeCustomFileTypePattern("folder/name.txt")).toBe("");
    expect(normalizeCustomFileTypePattern("!important.txt")).toBe("");
    expect(normalizeCustomFileTypePattern("*.PST")).toBe("*.PST");
    expect(normalizeCustomFileTypePattern("ipc-socket")).toBe("ipc-socket");
    // A duplicate add must be detectable without treating it as invalid input.
    const exclude = updateExcludePatterns("", ["*.pst"], true);
    expect(isExcludePatternEnabled(exclude, "*.pst")).toBe(true);
    expect(normalizeCustomFileTypePattern("*.pst")).toBe("*.pst");
  });

  it("keeps user comments untouched when toggling rules", () => {
    const withComment = "# 这是说明\n*.tmp";
    expect(updateExcludePatterns(withComment, ["*.tmp"], false)).toBe(
      "# 这是说明",
    );
    expect(updateExcludePatterns(withComment, ["*.log"], true)).toBe(
      "# 这是说明\n*.tmp\n*.log",
    );
  });
  it("rejects missing paths, inverted sizes and invalid continuous polling ranges", () => {
    const namedForm = { ...defaultJobForm(7), remark: "测试任务" };
    expect(validateJobForm(namedForm)).toContain("源目录");
    const form = { ...namedForm, srcPath: ["/a"], dstPath: ["/b"] };
    expect(
      validateJobForm({ ...form, minFileSize: 4, maxFileSize: 2 }),
    ).toContain("最大");
    expect(
      validateJobForm({
        ...form,
        extraFlags: { ...form.extraFlags, syncPollIntervalSec: 5 },
      }),
    ).toContain("10–86400");
    expect(validateJobForm(form)).toBe("");
  });
  it("does not substitute another task for an explicit unknown task id", () => {
    const jobs = [{ id: 1 }, { id: 2 }];
    expect(selectJob(jobs, null)).toEqual({ id: 1 });
    expect(selectJob(jobs, 999)).toBeUndefined();
  });

  it("falls back to the first task for a stale id and asks to rewrite the URL", () => {
    const jobs = [{ id: 1 }, { id: 2 }];
    expect(resolveJobSelection(jobs, 2)).toEqual({ job: { id: 2 }, replaceWith: null });
    expect(resolveJobSelection(jobs, null)).toEqual({ job: { id: 1 }, replaceWith: null });
    // deleted elsewhere / stale bookmark: show jobs[0] and point the URL at it
    expect(resolveJobSelection(jobs, 999)).toEqual({ job: { id: 1 }, replaceWith: 1 });
    // malformed id sentinel stays an explicit empty selection
    expect(resolveJobSelection(jobs, -1)).toEqual({ job: undefined, replaceWith: null });
    expect(resolveJobSelection([], 5)).toEqual({ job: undefined, replaceWith: null });
  });

  it("does not clobber a newly created task selection while the list is still refreshing", () => {
    const staleList = [{ id: 1 }];
    // Newly created job #2 is not yet in staleList; pendingId holds URL intact.
    expect(resolveJobSelection(staleList, 2, { pendingId: 2 })).toEqual({
      job: undefined,
      replaceWith: null,
    });
    expect(resolveJobSelection(staleList, 2, { refreshing: true })).toEqual({
      job: undefined,
      replaceWith: null,
    });
    // Once fresh list arrives, job #2 is selected cleanly.
    expect(resolveJobSelection([{ id: 1 }, { id: 2 }], 2, { pendingId: 2 })).toEqual({
      job: { id: 2 },
      replaceWith: null,
    });
  });

  it("identifies move-mode jobs affected by Dashboard batch runs", () => {
    const jobs = [
      { id: 1, method: 0, enable: 1, remark: "增量" },
      { id: 2, method: 2, enable: 1, remark: "移动启用" },
      { id: 3, method: 2, enable: 0, remark: "移动停用" },
    ];
    expect(findBatchMoveJobs("run_all", jobs).map((j) => j.id)).toEqual([2]);
    expect(findBatchMoveJobs("run", jobs, [1, 3]).map((j) => j.id)).toEqual([3]);
    expect(findBatchMoveJobs("run", jobs, [1]).map((j) => j.id)).toEqual([]);
    expect(findBatchMoveJobs("stop_all", jobs)).toEqual([]);
  });

  it("keeps explicit realtime and history tabs stable without auto-jumping on short sync batches", () => {
    const idle = { settled: true, hasCurrentTask: false };
    expect(parseTaskTab(null)).toBeNull();
    expect(parseTaskTab("invalid")).toBeNull();
    expect(parseTaskTab("realtime")).toBe("realtime");

    expect(resolveTaskTab("realtime", idle)).toBe("realtime");
    expect(resolveTaskTab("realtime", { settled: true, hasCurrentTask: true })).toBe(
      "realtime",
    );
    expect(resolveTaskTab("history", idle)).toBe("history");
    expect(
      resolveTaskTab(null, { settled: true, hasCurrentTask: true, jobRunning: true }),
    ).toBe("overview");
    expect(
      resolveTaskTab(null, { settled: false, hasCurrentTask: false, jobRunning: true }),
    ).toBe("overview");
    expect(resolveTaskTab("overview", { settled: true, hasCurrentTask: true })).toBe(
      "overview",
    );
  });

  it("builds task navigation params that open Cloud Sync overview and preserve history", () => {
    expect(
      buildTaskSearchParams({ id: 2, running: true }, { onTasksPage: false }).toString(),
    ).toBe("jobId=2");
    expect(
      buildTaskSearchParams({ id: 2, running: false }, { onTasksPage: false }).toString(),
    ).toBe("jobId=2");

    // Switching from history to another job keeps history
    expect(
      buildTaskSearchParams(
        { id: 2, running: true },
        { onTasksPage: true, currentSearch: "jobId=1&tab=history", activeJobId: 1 },
      ).toString(),
    ).toBe("jobId=2&tab=history");

    // Switching from overview or realtime to another job lands cleanly on overview
    expect(
      buildTaskSearchParams(
        { id: 2, running: false },
        { onTasksPage: true, currentSearch: "jobId=1&tab=overview", activeJobId: 1 },
      ).toString(),
    ).toBe("jobId=2");
    expect(
      buildTaskSearchParams(
        { id: 2, running: false },
        { onTasksPage: true, currentSearch: "jobId=1&tab=realtime", activeJobId: 1 },
      ).toString(),
    ).toBe("jobId=2");

    // Re-clicking the currently selected job keeps the user's chosen tab
    expect(
      buildTaskSearchParams(
        { id: 1, running: true },
        { onTasksPage: true, currentSearch: "jobId=1&tab=overview", activeJobId: 1 },
      ).toString(),
    ).toBe("jobId=1&tab=overview");
  });

  it("updates generated folder exclude rules without changing manual rules", () => {
    const manualRules = "*.tmp\nimportant.tmp";
    const withFolders = updateExcludeFolders(manualRules, [
      "photos/raw/",
      "cache/",
    ]);

    expect(withFolders).toContain(manualRules);
    expect(parseExcludeFolders(withFolders)).toEqual([
      "photos/raw/",
      "cache/",
    ]);
    expect(updateExcludeFolders(withFolders, ["logs/"])).toBe(
      "*.tmp\nimportant.tmp\n\n# --- 文件夹过滤（自动生成，请勿手动编辑）---\nlogs/\n# --- 文件夹过滤结束 ---",
    );
    expect(updateExcludeFolders(withFolders, [])).toBe(manualRules);
  });

  // 后端保存时会去掉 "# " 注释，标记块随之消失（issue #4）
  it("recognizes folder rules after the backend strips the marker block", () => {
    const saved =
      ".DS_Store\n.Trashes/\n.Trash-*/\n*.tmp\nphotos/raw/\ncache/";

    expect(parseExcludeFolders(saved)).toEqual(["photos/raw/", "cache/"]);

    const updated = updateExcludeFolders(saved, ["cache/"]);
    expect(updated).toBe(
      ".DS_Store\n.Trashes/\n.Trash-*/\n*.tmp\n\n# --- 文件夹过滤（自动生成，请勿手动编辑）---\ncache/\n# --- 文件夹过滤结束 ---",
    );
    expect(parseExcludeFolders(updated)).toEqual(["cache/"]);
    expect(updateExcludeFolders(saved, [])).toBe(
      ".DS_Store\n.Trashes/\n.Trash-*/\n*.tmp",
    );
  });

  it("does not treat system directory presets or disabled lines as folders", () => {
    const exclude = defaultJobForm().exclude + "\n# cache/";
    expect(parseExcludeFolders(exclude)).toEqual([]);
    expect(updateExcludeFolders(exclude, [])).toBe(exclude);
  });

  it("returns field-level errors, specific custom rule messages, source-change cleanup, and overview filter summaries", () => {
    const base = {
      ...defaultJobForm(7),
      remark: "测试",
      srcPath: ["/Photos"],
      dstPath: ["/Backup"],
    };

    const enabledEmptySize = taskFormModule.validateJobFormFields(
      { ...base, minFileSize: "" },
      { sizeEnabled: { min: true, max: false } },
    );
    expect(enabledEmptySize.fields.minFileSize).toContain("大于 0");
    expect(enabledEmptySize.stepErrors[2]).toContain("大于 0");

    expect(taskFormModule.validateCustomFilePattern("dir/a.txt").error).toContain(
      "路径分隔符",
    );
    expect(taskFormModule.validateCustomFilePattern("!keep.txt").error).toContain(
      "取反",
    );
    expect(taskFormModule.validateCustomFilePattern("**/*.log").error).toContain(
      "路径分隔符",
    );
    expect(taskFormModule.validateCustomFilePattern("*.log")).toEqual({
      pattern: "*.log",
      error: "",
    });

    const excludeWithFolder = updateExcludeFolders("*.tmp", ["raw/"]);
    expect(
      taskFormModule.shouldClearExcludeFoldersOnSrcChange(
        ["/Photos"],
        ["/Photos", "/Docs"],
        excludeWithFolder,
      ),
    ).toBe(false);
    expect(
      taskFormModule.shouldClearExcludeFoldersOnSrcChange(
        ["/Photos"],
        ["/Videos"],
        excludeWithFolder,
      ),
    ).toBe(true);

    expect(
      taskFormModule.summarizeJobFilters({
        minFileSize: 10 * 1024,
        maxFileSize: 2 * 1024 ** 3,
        exclude: `${defaultJobForm().exclude}\nraw/`,
      }),
    ).toEqual(
      expect.arrayContaining([
        "排除 < 10.00 KB",
        "排除 > 2.00 GB",
        "排除目录 1 个",
      ]),
    );
  });

  it("validates bisync single source/destination and round-trips rclone tuning fields", () => {
    const multiSrcBisync = {
      ...defaultJobForm(7),
      remark: "双向",
      method: 3,
      srcPath: ["/A", "/B"],
      dstPath: ["/C"],
    };
    expect(validateJobForm(multiSrcBisync)).toContain("一个源目录");

    const multiDstBisync = {
      ...defaultJobForm(7),
      remark: "双向",
      method: 3,
      srcPath: ["/A"],
      dstPath: ["/C", "/D"],
    };
    expect(validateJobForm(multiDstBisync)).toContain("一个目标目录");

    const stored = {
      id: 9,
      enable: 1,
      remark: "原生调优任务",
      alistId: 7,
      method: 3,
      isCron: 2,
      interval: 0,
      srcPath: '["/本地/Photos"]',
      dstPath: '["/Archive/Photos"]',
      useCacheS: 0,
      useCacheT: 0,
      bwLimit: "08:00,5M 23:00,off",
      tpsLimit: 12.5,
      tpsLimitBurst: 25,
      modifyWindow: "2s",
      fastList: 1,
      multiThreadStreams: 8,
      createEmptySrcDirs: 1,
      followSymlinks: 1,
      extraFlags: JSON.stringify({
        noUpdateModtime: true,
        inplace: false,
        checkFirst: true,
        s3NoHead: true,
      }),
    };
    const form = jobToForm(stored);
    expect(validateJobForm(form)).toBe("");
    expect(form.method).toBe(3);
    expect(form.bwLimit).toBe("08:00,5M 23:00,off");
    expect(form.tpsLimit).toBe(12.5);
    expect(form.tpsLimitBurst).toBe(25);
    expect(form.modifyWindow).toBe("2s");
    expect(form.fastList).toBe(true);
    expect(form.multiThreadStreams).toBe(8);
    expect(form.createEmptySrcDirs).toBe(true);
    expect(form.followSymlinks).toBe(true);
    expect(form.extraFlags).toMatchObject({
      noUpdateModtime: true,
      inplace: false,
      checkFirst: true,
      s3NoHead: true,
    });

    const extendedForm = jobToForm({
      ...stored,
      extraFlags: JSON.stringify({
        noUpdateModtime: true,
        ignoreSize: true,
        trackRenames: true,
        useMmap: true,
        maxTransfer: "50G",
        cutoffMode: "soft",
        maxDuration: "2h",
        orderBy: "size,ascending",
        maxBacklog: 1000,
        bufferSize: "32M",
        multiThreadCutoff: "100M",
        multiThreadChunkSize: "16M",
        userAgent: "OpenSync/1.0",
        ioTimeout: "3m",
        conTimeout: "30s",
        bisyncMaxDelete: 25,
        bisyncForce: true,
      }),
    });
    expect(JSON.parse(buildJobPayload(extendedForm).extraFlags)).toMatchObject({
      noUpdateModtime: true,
      ignoreSize: true,
      trackRenames: true,
      useMmap: true,
      maxTransfer: "50G",
      cutoffMode: "soft",
      maxDuration: "2h",
      orderBy: "size,ascending",
      maxBacklog: 1000,
      bufferSize: "32M",
      multiThreadCutoff: "100M",
      multiThreadChunkSize: "16M",
      userAgent: "OpenSync/1.0",
      ioTimeout: "3m",
      conTimeout: "30s",
      bisyncMaxDelete: 25,
      bisyncForce: true,
    });

    const payload = buildJobPayload(form);
    expect(payload).toMatchObject({
      method: 3,
      bwLimit: "08:00,5M 23:00,off",
      tpsLimit: 12.5,
      tpsLimitBurst: 25,
      modifyWindow: "2s",
      fastList: 1,
      multiThreadStreams: 8,
      createEmptySrcDirs: 1,
      followSymlinks: 1,
    });
    expect(JSON.parse(payload.extraFlags)).toEqual({
      noUpdateModtime: true,
      checkFirst: true,
      s3NoHead: true,
    });
  });

  it("clamps tpsLimit/tpsLimitBurst to 1000, bisyncMaxDelete to 100, rejects equal day bounds, and detects legacy OpenList jobs", () => {
    const clampedPayload = buildJobPayload({
      ...defaultJobForm(7),
      remark: "限流上限",
      srcPath: ["/A"],
      dstPath: ["/B"],
      tpsLimit: 5000,
      tpsLimitBurst: 9999,
      extraFlags: {
        ...defaultJobForm(7).extraFlags,
        bisyncMaxDelete: 250,
      },
    });
    expect(clampedPayload.tpsLimit).toBe(1000);
    expect(clampedPayload.tpsLimitBurst).toBe(1000);
    expect(JSON.parse(clampedPayload.extraFlags)).toEqual({
      bisyncMaxDelete: 100,
    });

    const equalDayBounds = validateJobForm({
      ...defaultJobForm(7),
      remark: "时间区间",
      srcPath: ["/A"],
      dstPath: ["/B"],
      olderThanDays: 7,
      newerThanDays: 7,
    });
    expect(equalDayBounds).toContain("必须小于");

    expect(
      taskFormModule.isLegacyOpenListJob({ legacyOpenList: true, alistId: 1 }),
    ).toBe(true);
    expect(
      taskFormModule.isLegacyOpenListJob({ alistId: 2 }, [{ id: 1 }]),
    ).toBe(true);
    expect(
      taskFormModule.isLegacyOpenListJob({ alistId: 1 }, [{ id: 1 }]),
    ).toBe(false);
  });
});
