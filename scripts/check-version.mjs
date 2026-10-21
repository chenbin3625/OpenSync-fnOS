#!/usr/bin/env node
// 用法：node scripts/check-version.mjs [tag]
// 不给 tag 时只检查各处版本号互相一致；CI 传入 GITHUB_REF_NAME。
import { collectVersions, versionProblems } from "./lib/version.mjs";

const tag = process.argv[2];
const versions = collectVersions();
console.log(
  Object.entries({ tag, ...versions })
    .filter(([, value]) => value !== undefined)
    .map(([key, value]) => `${key}=${value}`)
    .join(" "),
);
const problems = versionProblems(versions, tag);
for (const problem of problems)
  console.error(process.env.GITHUB_ACTIONS ? `::error::${problem}` : `✗ ${problem}`);
process.exit(problems.length > 0 ? 1 : 0);
