export function historyRangeParams(range: Date[]) {
  const start = range[0];
  const end = range[1];
  const startDay = start
    ? new Date(start.getFullYear(), start.getMonth(), start.getDate())
    : undefined;
  const endNextDay = end
    ? new Date(end.getFullYear(), end.getMonth(), end.getDate() + 1)
    : undefined;
  return {
    startTime: startDay
      ? Math.floor(startDay.getTime() / 1000)
      : undefined,
    endTimeExclusive: endNextDay
      ? Math.floor(endNextDay.getTime() / 1000)
      : undefined,
  };
}
