import { describe, expect, it } from "vitest";
import { isMockApiMode } from "../src/mocks/config";

describe("mock mode config", () => {
  it("starts mock APIs only when VITE_DATA_MODE is mock", () => {
    expect(isMockApiMode({ VITE_DATA_MODE: "mock" })).toBe(true);
    expect(isMockApiMode({ VITE_DATA_MODE: "real" })).toBe(false);
    expect(isMockApiMode({})).toBe(false);
  });
});
