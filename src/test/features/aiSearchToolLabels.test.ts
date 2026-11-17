import { describe, expect, it } from "vitest";
import { toolLabel } from "../../features/ai/panel/aiConfirmLabels";
import { toolCategory } from "../../features/ai/toolGroups";

describe("search tool labels", () => {
  it("names the search tools in words instead of raw tool names", () => {
    expect(toolLabel("find_files")).toBe("查找文件");
    expect(toolLabel("grep_files")).toBe("搜索文件内容");
    // History rows of the deprecated tool still get a readable label.
    expect(toolLabel("search_files")).toBe("搜索文件");
    expect(toolLabel("unknown_tool")).toBe("unknown_tool");
  });

  it("groups every search tool under search", () => {
    for (const name of ["find_files", "grep_files", "search_files"]) expect(toolCategory(name)).toBe("search");
  });
});
