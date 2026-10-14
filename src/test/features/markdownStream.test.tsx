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

describe("Markdown streaming tail stability", () => {
  it("renders an unfinished trailing line as plain text while streaming without changing its structure early", () => {
    render("结果如下：\n\n| 容器 | 状态 |", true);
    expect(view!.container.querySelector("table")).toBeNull();
    expect(view!.container.textContent).toContain("| 容器 | 状态 |");
  });

  it("renders the table only after the separator row and a newline arrive", () => {
    render("Results:\n\n| Container | Status |\n| --- | --- |\n", true);
    expect(view!.container.querySelector("table")).not.toBeNull();
    expect(view!.container.querySelectorAll("th")).toHaveLength(2);
  });

  it("keeps the streaming tail out of table rows and merges it only after a newline", () => {
    render("结果如下：\n\n| 容器 | 状态 |\n| --- | --- |\n| mysql | 运行", true);
    const table = view!.container.querySelector("table");
    expect(table?.textContent).not.toContain("mysql");
    expect(view!.container.textContent).toContain("| mysql | 运行");
  });

  it("renders the table immediately for complete non-streaming text", () => {
    render("Results:\n\n| Container | Status |\n| --- | --- |\n| mysql | running", false);
    const table = view!.container.querySelector("table");
    expect(table?.textContent).toContain("mysql");
  });

  it("does not render unclosed inline emphasis in the streaming tail and renders it only after completion", () => {
    render("这是 **加粗", true);
    expect(view!.container.querySelector("strong")).toBeNull();
    render("这是 **加粗** 的文字", true);
    expect(view!.container.querySelector("strong")).toBeNull();
    render("这是 **加粗** 的文字", false);
    expect(view!.container.querySelector("strong")?.textContent).toBe("加粗");
  });

  it("does not turn a list marker in the streaming tail into a list early", () => {
    render("- First item", true);
    expect(view!.container.querySelector("ul")).toBeNull();
    render("- First item\n- Second item\n", true);
    expect(view!.container.querySelectorAll("ul li")).toHaveLength(2);
  });

  it("keeps code fence content as plain-text increments while streaming", () => {
    render("```js\nconst a = 1;\nconst b", true);
    const code = view!.container.querySelector("pre code");
    expect(code?.textContent).toContain("const a = 1;");
    expect(view!.container.textContent).toContain("const b");
  });
});
