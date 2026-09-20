import { request } from "./client";
import type { RequestOptions } from "./request";
import type {
  CurrentTaskData,
  PageData,
  TaskItem,
} from "../types";

export async function jobGetTaskCurrent(
  params: Record<string, unknown>,
  options?: RequestOptions,
): Promise<
  CurrentTaskData | PageData<TaskItem> | TaskItem[] | null
> {
  return request<
    CurrentTaskData | PageData<TaskItem> | TaskItem[] | null
  >("/job", { params: { ...params, current: 1 }, signal: options?.signal });
}
