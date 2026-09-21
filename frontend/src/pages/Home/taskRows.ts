import type {
  CurrentTaskData,
  PageData,
  RealtimeTaskItemPage,
  TaskItem,
} from "../../types";

type CurrentTaskIdentity =
  | {
      createTime?: number | string | null;
      taskId?: number | string | null;
    }
  | null
  | undefined;

export function getRealtimeTaskIdentity(task: CurrentTaskIdentity): string {
  return `${Number(task?.taskId || 0)}:${Number(task?.createTime || 0)}`;
}

type RealtimeTaskPageIdentity = {
  taskId?: number | string | null;
  createTime?: number | string | null;
  status?: number | string | null;
  pageNum?: number | string | null;
  pageSize?: number | string | null;
};

function sameNumber(
  actual: number | string | null | undefined,
  expected: number | string | null | undefined,
): boolean {
  if (expected === undefined || expected === null || expected === "") {
    return true;
  }
  if (actual === undefined || actual === null || actual === "") {
    return false;
  }
  return Number(actual) === Number(expected);
}

function taskItemMatchesStatus(item: TaskItem, status: number): boolean {
  return status === -1
    ? ![0, 1, 2, 7].includes(item.status)
    : item.status === status;
}

export function realtimeTaskPageMatches(
  page: RealtimeTaskItemPage,
  expected?: RealtimeTaskPageIdentity,
): boolean {
  if (page.stale) return false;
  if (!expected) return true;
  return (
    sameNumber(page.taskId, expected.taskId) &&
    sameNumber(page.createTime, expected.createTime) &&
    sameNumber(page.status, expected.status) &&
    sameNumber(page.pageNum, expected.pageNum) &&
    sameNumber(page.pageSize, expected.pageSize)
  );
}

export function normalizeTaskItemPage(
  data: CurrentTaskData | PageData<TaskItem> | TaskItem[] | null | undefined,
  expected?: RealtimeTaskPageIdentity,
): { rows: TaskItem[]; total: number } {
  if (!data) return { rows: [], total: 0 };
  if (Array.isArray(data)) {
    return expected ? { rows: [], total: 0 } : { rows: data, total: data.length };
  }
  if ("dataList" in data && Array.isArray(data.dataList)) {
    if (!realtimeTaskPageMatches(data as RealtimeTaskItemPage, expected)) {
      return { rows: [], total: 0 };
    }
    const expectedStatus = expected?.status;
    if (
      expectedStatus === undefined ||
      expectedStatus === null ||
      expectedStatus === ""
    ) {
      return { rows: data.dataList, total: Number(data.count || 0) };
    }
    const rows = data.dataList.filter((item) =>
      taskItemMatchesStatus(item, Number(expectedStatus)),
    );
    const rejectedCount = data.dataList.length - rows.length;
    return {
      rows,
      total: Math.max(rows.length, Number(data.count || 0) - rejectedCount),
    };
  }
  return { rows: [], total: 0 };
}

export function taskProgressPercent(progress: unknown): number {
  const value = Number(progress);
  if (!Number.isFinite(value)) return 0;
  return Math.max(0, Math.min(100, Math.round(value)));
}

type ProgressSnapshot = {
  duration?: number;
  doneSize?: number;
} | null;

export function calcRealtimeProgress(
  cur: CurrentTaskData,
  previous: ProgressSnapshot,
): {
  remainSize: number;
  doneSize: number;
  speed: number;
  speedAvg: number;
  remainTime: number;
} {
  const sizeMap = cur.size || {};
  const serverDone = Number(cur.doneSize);
  const serverRemain = Number(cur.remainSize);
  let doneSize: number;
  let remainSize: number;
  if (Number.isFinite(serverDone) && Number.isFinite(serverRemain)) {
    doneSize = serverDone;
    remainSize = Math.max(0, serverRemain);
  } else {
    const doingSize = (cur.doingTask || []).reduce((sum, item) => {
      const progress = Number(item.progress || 0);
      return sum + ((item.fileSize || 0) * progress) / 100.0;
    }, 0);
    remainSize = Math.max(
      0,
      (sizeMap.running || 0) - doingSize + (sizeMap.wait || 0),
    );
    doneSize = (sizeMap.success || 0) + doingSize;
  }

  let speed = 0;
  if (typeof cur.speed === "number" && Number.isFinite(cur.speed)) {
    speed = cur.speed;
  } else if (
    previous &&
    typeof previous.duration === "number" &&
    cur.duration !== previous.duration
  ) {
    speed =
      (doneSize - (previous.doneSize || 0)) /
      (cur.duration - previous.duration);
  }

  let speedAvg = 0;
  if (typeof cur.speedAvg === "number" && Number.isFinite(cur.speedAvg)) {
    speedAvg = cur.speedAvg;
  } else if (cur.firstSync && cur.duration > 0) {
    const syncDuration = cur.duration - (cur.firstSync - cur.createTime);
    if (syncDuration > 0) speedAvg = doneSize / syncDuration;
  }

  let remainTime = 0;
  if (typeof cur.remainTime === "number" && Number.isFinite(cur.remainTime)) {
    remainTime = cur.remainTime;
  } else if (speedAvg > 0 && remainSize > 0) {
    remainTime = Math.ceil(remainSize / speedAvg);
  }

  return { remainSize, doneSize, speed, speedAvg, remainTime };
}
