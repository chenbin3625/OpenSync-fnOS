import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const stylesCss = readFileSync(
  new URL("../src/styles.css", import.meta.url),
  "utf8",
);

describe("typography and style tokens", () => {
  it("defines a native Latin-first UI font stack without unbundled Inter", () => {
    const match = /--font-sans:\s*([^;]+);/.exec(stylesCss);
    expect(match).not.toBeNull();
    const stack = match![1].replace(/\s+/g, " ").trim();

    expect(stack).not.toMatch(/\bInter\b/i);
    expect(stack.indexOf('"Segoe UI"')).toBeGreaterThan(-1);
    expect(stack.indexOf('"Microsoft YaHei"')).toBeGreaterThan(-1);
    expect(stack.indexOf('"PingFang SC"')).toBeGreaterThan(-1);
    // Latin UI fonts must precede CJK fonts so English text on Windows/Linux
    // never falls back into Microsoft YaHei or other CJK Latin glyphs.
    expect(stack.indexOf('"Segoe UI"')).toBeLessThan(
      stack.indexOf('"PingFang SC"'),
    );
    expect(stack.indexOf('"Segoe UI"')).toBeLessThan(
      stack.indexOf('"Microsoft YaHei"'),
    );
    expect(stack.indexOf("Arial")).toBeLessThan(
      stack.indexOf('"PingFang SC"'),
    );
  });

  it("overrides Semi UI font-family rules and uses only tokenized font sizes", () => {
    expect(stylesCss).toContain("font-family: var(--font-sans);");
    expect(stylesCss).not.toContain("font-synthesis: none;");
    expect(stylesCss).not.toMatch(/font-size:\s*(10|11|13)px\s*;/);
  });

  it("does not apply monospace font to regular UI paths, URLs, or filter rules", () => {
    const uiFiles = [
      "../src/components/common.tsx",
      "../src/components/ExcludeTree.tsx",
      "../src/components/FileTypeFilter.tsx",
      "../src/components/SyncPathPreview.tsx",
      "../src/pages/TaskEditor.tsx",
      "../src/pages/TaskExecution.tsx",
      "../src/pages/Tasks.tsx",
    ];
    for (const relPath of uiFiles) {
      const source = readFileSync(new URL(relPath, import.meta.url), "utf8");
      // Info's optional `mono` prop stays for API compatibility, so only check
      // actual JSX className strings containing the `mono` utility class.
      expect(source, relPath).not.toMatch(/className="[^"]*\bmono\b[^"]*"/);
    }
    const sysDirRule = /\.system-dir-patterns\s*\{(?<body>[^}]+)\}/.exec(
      stylesCss,
    )?.groups?.body;
    expect(sysDirRule).toBeDefined();
    expect(sysDirRule).not.toContain("font-family");
  });
});
