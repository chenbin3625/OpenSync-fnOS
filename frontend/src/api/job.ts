import { request } from "./client";
import type { RequestOptions } from "./request";
import type {
  CurrentTaskData,
  PageData,
  RealtimeTaskItemPage,
  TaskItem,
} from "../types";

export async function jobGetTaskCurrent(
  params: Record<string, unknown>,
  options?: RequestOptions,
): Promise<
  CurrentTaskData | RealtimeTaskItemPage | PageData<TaskItem> | TaskItem[] | null
> {
  return request<
    CurrentTaskData | RealtimeTaskItemPage | PageData<TaskItem> | TaskItem[] | null
  >("/job", { params: { ...params, current: 1 }, signal: options?.signal });
}
