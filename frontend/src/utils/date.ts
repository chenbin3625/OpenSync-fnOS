import dayjs from "dayjs";

/** 把后端返回的秒级时间戳格式化为卡片副标题里的短时间文本。 */
export function formatTimestamp(value?: number | null): string {
  const seconds = Number(value || 0);
  return seconds > 0 ? dayjs.unix(seconds).format("YYYY-MM-DD HH:mm") : "—";
}
