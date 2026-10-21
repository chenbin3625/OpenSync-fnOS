import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";

function source(path: string) {
  return readFileSync(new URL(path, import.meta.url), "utf8");
}

describe("mock architecture", () => {
  it("keeps mock and demo data out of page rendering code", () => {
    const tasks = source("../src/pages/Tasks.tsx");
    const execution = source("../src/pages/TaskExecution.tsx");

    expect(tasks).not.toContain("import.meta.env.DEV");
    expect(execution).not.toContain("import.meta.env.DEV");
    expect(execution).not.toContain("demoTaskData");
    expect(execution).not.toContain("createDemoTask");
  });

  it("only imports the MSW worker behind the DEV gate", () => {
    // Without import.meta.env.DEV the mock worker (and its chunk) would be
    // shipped inside the go:embed payload although it must never be reachable
    // from a release build.
    const main = source("../src/main.tsx");
    expect(main).toContain("import.meta.env.DEV");
    expect(main).toContain("isMockApiMode()");
    expect(main).toContain('import("./mocks/browser")');
  });

  it("keeps request fallback logic out of the API client", () => {
    const client = source("../src/api/client.ts");

    expect(client).not.toContain("devFallback");
    expect(client).not.toContain("fallback =");
  });
});
