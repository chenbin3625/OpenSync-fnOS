// 版本一致性检查：fnos/manifest、两个 package.json、README 当前版本必须一致；
// 给出 tag 时还必须与 tag 一致。CI（release.yml）与本地 release.mjs 共用这一实现，
// 避免本地打出版本号不一致的包。
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { root } from "./toolchain.mjs";

export const SEMVER_PATTERN = /^\d+\.\d+\.\d+$/;

const read = (file) =>
  readFileSync(join(root, file), "utf8").replace(/\r\n/g, "\n").replace(/\r/g, "\n");

export const parseManifestVersion = (content) => {
  const normalized = content.replace(/\r\n/g, "\n").replace(/\r/g, "\n");
  return (/^version=(.+)$/m.exec(normalized) || [])[1]?.trim();
};

export const collectVersions = () => ({
  "fnos/manifest": parseManifestVersion(read("fnos/manifest")),
  "package.json": String(JSON.parse(read("package.json")).version ?? "").trim() || undefined,
  "frontend/package.json":
    String(JSON.parse(read("frontend/package.json")).version ?? "").trim() || undefined,
  "README.md": (/^当前版本：\*\*v([0-9][0-9.]*)\*\*$/m.exec(read("README.md")) || [])[1]?.trim(),
});

/** 返回不一致项的描述列表；空数组表示一致。tag 可带或不带前缀 v。 */
export const versionProblems = (versions, tag) => {
  const problems = [];
  const expected = tag ? tag.trim().replace(/^v/, "") : versions["fnos/manifest"];
  if (!versions["fnos/manifest"]) {
    problems.push("fnos/manifest 未声明 version");
  } else if (!SEMVER_PATTERN.test(versions["fnos/manifest"])) {
    problems.push(`fnos/manifest 版本格式无效 (${versions["fnos/manifest"]})，应为 X.Y.Z`);
  }
  if (tag && expected && !SEMVER_PATTERN.test(expected)) {
    problems.push(`Git tag (${tag}) 版本格式无效，应为 vX.Y.Z 或 X.Y.Z`);
  }
  for (const [source, value] of Object.entries(versions)) {
    if (!value) {
      if (source !== "fnos/manifest") problems.push(`${source} 未声明版本`);
      continue;
    }
    if (!SEMVER_PATTERN.test(value)) {
      if (source !== "fnos/manifest")
        problems.push(`${source} 版本格式无效 (${value})，应为 X.Y.Z`);
      continue;
    }
    if (expected && value !== expected)
      problems.push(
        tag
          ? `Git tag (${expected}) 与 ${source} (${value}) 版本不一致`
          : `${source} (${value}) 与 fnos/manifest (${expected}) 版本不一致`,
      );
  }
  return problems;
};
