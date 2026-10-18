import { describe, expect, it } from "vitest";
import type { TreeNodeData } from "@douyinfe/semi-ui/lib/es/tree/interface";
import {
  collectTreeKeys,
  patchTreeNodes,
  sortPathsByDepth,
  splitDirPath,
  toTreeChildren,
} from "../src/lib/treeUtils";

const node = (key: string, children?: TreeNodeData[]): TreeNodeData => ({
  label: key.split("/").pop() || "/",
  value: key,
  key,
  isLeaf: false,
  ...(children ? { children } : {}),
});

describe("patchTreeNodes", () => {
  it("keeps loaded subtrees when the parent is reloaded", () => {
    // 编辑任务时预置的路径链已展开加载，此时根目录请求才返回
    const tree = [
      node("/", [node("/Photos", [node("/Photos/Albums"), node("/Photos/Raw")])]),
    ];
    const next = patchTreeNodes(
      tree,
      "/",
      toTreeChildren("/", [{ name: "Photos" }, { name: "Backup" }]),
    );
    expect(next[0].children?.map((n) => n.key)).toEqual(["/Photos", "/Backup"]);
    expect(next[0].children?.[0].children?.map((n) => n.key)).toEqual([
      "/Photos/Albums",
      "/Photos/Raw",
    ]);
  });

  it("drops directories that no longer exist", () => {
    const tree = [node("/", [node("/Old", [node("/Old/a")]), node("/Keep")])];
    const next = patchTreeNodes(tree, "/", toTreeChildren("/", [{ name: "Keep" }]));
    expect([...collectTreeKeys(next)]).toEqual(["/", "/Keep"]);
  });

  it("marks a node without children as leaf", () => {
    const next = patchTreeNodes([node("/")], "/", []);
    expect(next[0].isLeaf).toBe(true);
  });

  it("patches nested nodes", () => {
    const tree = [node("/", [node("/A", [node("/A/B")])])];
    const next = patchTreeNodes(tree, "/A/B", toTreeChildren("/A/B", [{ path: "C" }]));
    expect([...collectTreeKeys(next)]).toEqual(["/", "/A", "/A/B", "/A/B/C"]);
  });
});

describe("sortPathsByDepth", () => {
  it("orders parents before children", () => {
    expect(sortPathsByDepth(["/A/B", "/", "/C", "/A"])).toEqual([
      "/",
      "/A",
      "/C",
      "/A/B",
    ]);
  });
});

describe("splitDirPath", () => {
  it("splits parent and last segment", () => {
    expect(splitDirPath("/a/b/c")).toEqual({ parent: "/a/b/", name: "c" });
    expect(splitDirPath("/a")).toEqual({ parent: "/", name: "a" });
    expect(splitDirPath("/a/b/")).toEqual({ parent: "/a/", name: "b" });
    expect(splitDirPath("/")).toEqual({ parent: "", name: "/" });
  });
});
