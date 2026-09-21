import type { CurrentTaskView, TaskItem, TaskRecord, TaskNumKey } from "../../types";

const DEMO_ROWS_PER_GROUP = 120;

const sourceRoots = [
  "/photos/2026/events",
  "/documents/team/reports",
  "/videos/projects",
  "/database/backups",
  "/archive/client-assets",
  "/media/raw",
];

const fileNames = [
  "summer-vacation-photos-collection-final.zip",
  "department-review-report.pdf",
  "family-album-high-resolution.jpg",
  "system-backup-incremental.tar.gz",
  "weekly-standup-recording.mp4",
  "production-database-dump.sql.gz",
  "design-assets-export.sketch",
  "invoice-batch-q4.xlsx",
  "source-material-package.mov",
  "release-artifact-bundle.tar",
];

const groupStatuses = [0, 1, 2, 7, 5] as const;

function itemGroupName(status: number): TaskNumKey {
  if (status === 0) return "wait";
  if (status === 1) return "running";
  if (status === 2) return "success";
  if (status === 7) return "fail";
  return "other";
}

function fileSize(index: number): number {
  return (16 + (index % 96)) * 1024 * 1024;
}

function progressForStatus(status: number, index: number): number {
  if (status === 2) return 100;
  if (status === 1) return 8 + ((index * 7) % 86);
  if (status === 7) return (index * 11) % 72;
  if (status === 5) return (index * 5) % 64;
  return 0;
}

export function createDemoTaskItems(): TaskItem[] {
  const rows: TaskItem[] = [];

  for (const status of groupStatuses) {
    for (let i = 0; i < DEMO_ROWS_PER_GROUP; i++) {
      const id = rows.length + 1;
      const root = sourceRoots[i % sourceRoots.length];
      const fileName = `${String(id).padStart(4, "0")}-${fileNames[i % fileNames.length]}`;
      rows.push({
        id,
        fileName,
        srcPath: `${root}/${String(Math.floor(i / 12) + 1).padStart(2, "0")}`,
        dstPath: `/backup${root}/${String(Math.floor(i / 12) + 1).padStart(2, "0")}`,
        fileSize: fileSize(id),
        type: i % 17 === 0 ? 2 : 1,
        status,
        progress: progressForStatus(status, i),
        errMsg:
          status === 7
            ? i % 2 === 0
              ? "连接超时：目标引擎无响应，请检查引擎地址和网络连接"
              : "写入失败：目标磁盘空间不足"
            : undefined,
        createTime: 1_800_000_000 - id * 30,
      });
    }
  }

  return rows;
}

function countByGroup(items: TaskItem[]): Record<TaskNumKey, number> {
  return items.reduce<Record<TaskNumKey, number>>(
    (counts, item) => {
      counts[itemGroupName(item.status)] += 1;
      return counts;
    },
    { wait: 0, running: 0, success: 0, fail: 0, other: 0 },
  );
}

function sizeByGroup(items: TaskItem[]): Record<TaskNumKey, number> {
  return items.reduce<Record<TaskNumKey, number>>(
    (sizes, item) => {
      sizes[itemGroupName(item.status)] += item.fileSize || 0;
      return sizes;
    },
    { wait: 0, running: 0, success: 0, fail: 0, other: 0 },
  );
}

export function createDemoTaskView(
  now = Math.floor(Date.now() / 1000),
  items = createDemoTaskItems(),
): CurrentTaskView {
  const sizes = sizeByGroup(items);
  const doneSize = sizes.success;
  const remainSize = sizes.wait + sizes.running + sizes.fail + sizes.other;

  return {
    taskId: 99999,
    scanFinish: true,
    createTime: now - 320,
    duration: 320,
    num: countByGroup(items),
    size: sizes,
    doneSize,
    remainSize,
    speed: 12.5 * 1024 * 1024,
    speedAvg: 8.7 * 1024 * 1024,
    remainTime: 52,
    doingTask: items.filter((item) => item.status === 1),
  };
}

export function createDemoTaskRecords(
  now = Math.floor(Date.now() / 1000),
): TaskRecord[] {
  return Array.from({ length: DEMO_ROWS_PER_GROUP }, (_, index) => {
    const status = index % 11 === 0 ? 8 : index % 5 === 0 ? 7 : 2;
    const allNum = 120 + ((index * 13) % 880);
    const failNum = status === 7 ? 1 + (index % 18) : 0;
    const successNum = status === 8 ? Math.floor(allNum * 0.6) : allNum - failNum;
    const createTime = now - (index + 1) * 86400;

    return {
      id: 9001 + index,
      status,
      errMsg:
        status === 7
          ? index % 2 === 0
            ? "连接超时：引擎无响应"
            : "磁盘空间不足"
          : undefined,
      createTime,
      runTime: createTime + 45 + ((index * 37) % 3600),
      successNum,
      failNum,
      allNum,
    };
  });
}

export function pageDemoRows<T>(rows: T[], page: number, pageSize: number): T[] {
  const safePage = Math.max(1, Math.floor(page));
  const safePageSize = Math.max(1, Math.floor(pageSize));
  const start = (safePage - 1) * safePageSize;
  return rows.slice(start, start + safePageSize);
}
