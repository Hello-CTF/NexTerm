/** @vitest-environment jsdom */

import { afterEach, describe, expect, it } from "vitest";
import { createElement } from "react";
import { mount, type MountedView } from "./reactTestUtils";
import { Markdown } from "../../features/ai/Markdown";

let view: MountedView | null = null;

function render(text: string, streaming = false): MountedView {
  view = mount(createElement(Markdown, { text, streaming }));
  return view;
}

afterEach(() => {
  view?.unmount();
  view = null;
});

describe("Markdown 流式尾部稳定", () => {
  it("流式进行中把未完成的尾行按纯文本渲染，不提前翻转结构", () => {
    render("结果如下：\n\n| 容器 | 状态 |", true);
    expect(view!.container.querySelector("table")).toBeNull();
    expect(view!.container.textContent).toContain("| 容器 | 状态 |");
  });

  it("分隔行到达并换行后表格才出现", () => {
    render("结果如下：\n\n| 容器 | 状态 |\n| --- | --- |\n", true);
    expect(view!.container.querySelector("table")).not.toBeNull();
    expect(view!.container.querySelectorAll("th")).toHaveLength(2);
  });

  it("流式尾部不进表格行，换行后才并入", () => {
    render("结果如下：\n\n| 容器 | 状态 |\n| --- | --- |\n| mysql | 运行", true);
    const table = view!.container.querySelector("table");
    expect(table?.textContent).not.toContain("mysql");
    expect(view!.container.textContent).toContain("| mysql | 运行");
  });

  it("非流式完整文本立即渲染表格", () => {
    render("结果如下：\n\n| 容器 | 状态 |\n| --- | --- |\n| mysql | 运行", false);
    const table = view!.container.querySelector("table");
    expect(table?.textContent).toContain("mysql");
  });

  it("流式尾部不渲染未闭合的行内强调，结束后才渲染", () => {
    render("这是 **加粗", true);
    expect(view!.container.querySelector("strong")).toBeNull();
    render("这是 **加粗** 的文字", true);
    expect(view!.container.querySelector("strong")).toBeNull();
    render("这是 **加粗** 的文字", false);
    expect(view!.container.querySelector("strong")?.textContent).toBe("加粗");
  });

  it("流式尾行的列表符号不提前成列表", () => {
    render("- 第一项", true);
    expect(view!.container.querySelector("ul")).toBeNull();
    render("- 第一项\n- 第二项\n", true);
    expect(view!.container.querySelectorAll("ul li")).toHaveLength(2);
  });

  it("流式代码围栏内容保持纯文本增量", () => {
    render("```js\nconst a = 1;\nconst b", true);
    const code = view!.container.querySelector("pre code");
    expect(code?.textContent).toContain("const a = 1;");
    expect(view!.container.textContent).toContain("const b");
  });
});
