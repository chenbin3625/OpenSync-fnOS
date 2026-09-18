import { request } from "./client";
import type { RequestOptions } from "./request";
import type {
  ApiResponse,
  CurrentTaskData,
  PageData,
  TaskItem,
} from "../types";

export async function jobGetTaskCurrent(
  params: Record<string, unknown>,
  options?: RequestOptions,
): Promise<
  ApiResponse<CurrentTaskData | PageData<TaskItem> | TaskItem[] | null>
> {
  const data = await request<
    CurrentTaskData | PageData<TaskItem> | TaskItem[] | null
  >("/job", { params: { ...params, current: 1 }, signal: options?.signal });
  return { code: 200, msg: "ok", data };
}
