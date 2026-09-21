import { request } from "./client";
import type {
  CurrentTaskData,
  PageData,
  RealtimeTaskItemPage,
  TaskItem,
} from "../types";

type RequestOptions = {
  signal?: AbortSignal;
};

export async function jobGetTaskCurrent(
  params: Record<string, unknown>,
  options?: RequestOptions,
): Promise<
  CurrentTaskData | RealtimeTaskItemPage | PageData<TaskItem> | TaskItem[] | null
> {
  return request<
    CurrentTaskData | RealtimeTaskItemPage | PageData<TaskItem> | TaskItem[] | null
  >("/job", {
    params: { ...params, current: 1 },
    signal: options?.signal,
    cache: "no-store",
  });
}
