export type FileSizeUnit = "B" | "KB" | "MB" | "GB" | "TB";
export type FileSizeInput = number | string;

export const fileSizeUnitOptions = [
  { value: "B", label: "B" },
  { value: "KB", label: "KB" },
  { value: "MB", label: "MB" },
  { value: "GB", label: "GB" },
  { value: "TB", label: "TB" },
] satisfies Array<{ value: FileSizeUnit; label: FileSizeUnit }>;

const fileSizeUnitFactors: Record<FileSizeUnit, number> = {
  B: 1,
  KB: 1024,
  MB: 1024 ** 2,
  GB: 1024 ** 3,
  TB: 1024 ** 4,
};

export function normalizeFileSizeUnit(unit?: string | null): FileSizeUnit {
  return fileSizeUnitOptions.some((option) => option.value === unit)
    ? (unit as FileSizeUnit)
    : "MB";
}

export function parseFileSizeInput(
  value?: FileSizeInput | null,
): number | null {
  if (value === null || value === undefined) return 0;
  if (typeof value === "string" && value.trim() === "") return 0;
  const parsed = Number(typeof value === "string" ? value.trim() : value);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : null;
}

export function fileSizeToBytes(
  value?: FileSizeInput | null,
  unit?: string | null,
): number {
  const normalized = parseFileSizeInput(value);
  if (normalized === null || normalized <= 0) return 0;
  return Math.round(
    normalized * fileSizeUnitFactors[normalizeFileSizeUnit(unit)],
  );
}

export function splitBytesToFileSize(bytes?: number | null): {
  value: number;
  unit: FileSizeUnit;
} {
  const normalized = Number(bytes || 0);
  if (!Number.isFinite(normalized) || normalized <= 0) {
    return { value: 0, unit: "MB" };
  }

  const units = [...fileSizeUnitOptions]
    .map((option) => option.value)
    .reverse();
  const unit =
    units.find((candidate) => normalized >= fileSizeUnitFactors[candidate]) ||
    "B";
  const value = normalized / fileSizeUnitFactors[unit];
  return {
    value: Number.isInteger(value) ? value : Number(value.toFixed(2)),
    unit,
  };
}
