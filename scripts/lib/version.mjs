// 版本一致性检查：fnos/manifest、两个 package.json、README 当前版本必须一致；
// 给出 tag 时还必须与 tag 一致。CI（release.yml）与本地 release.mjs 共用这一实现，
// 避免本地打出版本号不一致的包。
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { root } from "./toolchain.mjs";

const read = (file) => readFileSync(join(root, file), "utf8");

export const collectVersions = () => ({
  "fnos/manifest": (/^version=(.+)$/m.exec(read("fnos/manifest")) || [])[1]?.trim(),
  "package.json": JSON.parse(read("package.json")).version,
  "frontend/package.json": JSON.parse(read("frontend/package.json")).version,
  "README.md": (/^当前版本：\*\*v([0-9][0-9.]*)\*\*$/m.exec(read("README.md")) || [])[1],
});

/** 返回不一致项的描述列表；空数组表示一致。tag 可带或不带前缀 v。 */
export const versionProblems = (versions, tag) => {
  const problems = [];
  const expected = tag ? tag.replace(/^v/, "") : versions["fnos/manifest"];
  if (!versions["fnos/manifest"]) problems.push("fnos/manifest 未声明 version");
  for (const [source, value] of Object.entries(versions)) {
    if (!value) {
      if (source !== "fnos/manifest") problems.push(`${source} 未声明版本`);
      continue;
    }
    if (value !== expected)
      problems.push(
        tag
          ? `Git tag (${expected}) 与 ${source} (${value}) 版本不一致`
          : `${source} (${value}) 与 fnos/manifest (${expected}) 版本不一致`,
      );
  }
  return problems;
};
