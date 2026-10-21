import { describe, expect, it } from "vitest";
import {
  buildJobPayload,
  defaultJobForm,
  fileTypeFilterGroups,
  isExcludePatternEnabled,
  jobToForm,
  normalizeCustomFileTypePattern,
  parseExcludeFolders,
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
  it("keeps multi-source, multi-target and the two supported synchronization modes", () => {
    expect(methodNames).toEqual(["增量同步", "全量同步"]);
    expect(methodOptions.map((method) => method.description).join("\n")).toContain(
      "不会删除目标端多余文件",
    );
    expect(methodOptions.map((method) => method.description).join("\n")).toContain(
      "会删除目标端多余文件",
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

  it("expands selected source roots by default for folder filtering", () => {
    expect(initialExcludeExpandedKeys(["/Photos", "/Docs/"])).toEqual([
      "/Photos",
      "/Docs",
    ]);
    expect(normalizeExcludeRootPath("///")).toBe("/");
  });
  it("keeps manual-only jobs enabled and translates file size units", () => {
    const payload = buildJobPayload({
      ...defaultJobForm(7),
      isCron: 2,
      enable: false,
      minFileSize: 2,
      minFileSizeUnit: "GB",
    });
    expect(payload.enable).toBe(1);
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
  it("round trips existing job identifiers, cache settings and Cron expressions", () => {
    const job = {
      id: 8,
      alistId: 7,
      enable: 0,
      srcPath: '["/a","/b"]',
      dstPath: '["/c"]',
      remark: "test",
      method: 1,
      isCron: 1,
      interval: 33,
      useCacheS: 1,
      useCacheT: 0,
      scanIntervalS: 77,
      scanIntervalT: 88,
      minFileSize: 1024,
      maxFileSize: 4096,
      second: "0",
      minute: "*/5",
      hour: "*",
      day: "*",
      month: "*",
      day_of_week: "1-5",
      exclude: "*.tmp",
    };
    expect(buildJobPayload(jobToForm(job))).toMatchObject({
      ...job,
      srcPath: ["/a", "/b"],
      dstPath: ["/c"],
    });
  });
  it("preserves the legacy move method when editing (backend still implements it)", () => {
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

    // method 2 = move (copy + delete source) in backend/internal/service/
    // task_scan.go; silently normalizing it to 0 turned a move job into a copy.
    expect(form.method).toBe(2);
    expect(buildJobPayload(form).method).toBe(2);
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
  it("rejects missing paths, inverted sizes and invalid Cron ranges", () => {
    const namedForm = { ...defaultJobForm(7), remark: "测试任务" };
    expect(validateJobForm(namedForm)).toContain("源目录");
    const form = { ...namedForm, srcPath: ["/a"], dstPath: ["/b"] };
    expect(
      validateJobForm({ ...form, minFileSize: 4, maxFileSize: 2 }),
    ).toContain("最大");
    expect(validateJobForm({ ...form, hour: "25" })).toContain("时");
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

  it("never renders the disabled realtime tab once the task check settles", () => {
    const idle = { settled: true, hasCurrentTask: false };
    expect(resolveTaskTab("realtime", idle)).toBe("overview");
    expect(resolveTaskTab("realtime", { settled: false, hasCurrentTask: false })).toBe(
      "realtime",
    );
    expect(resolveTaskTab("realtime", { settled: true, hasCurrentTask: true })).toBe(
      "realtime",
    );
    expect(resolveTaskTab("history", idle)).toBe("history");
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
});
