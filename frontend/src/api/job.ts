import { requestCurrentTask } from "./client";
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
  return requestCurrentTask<
    CurrentTaskData | RealtimeTaskItemPage | PageData<TaskItem> | TaskItem[] | null
  >(params, options?.signal);
}
