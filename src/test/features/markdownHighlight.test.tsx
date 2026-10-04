/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flush, flushUntil, mount, type MountedView } from "./reactTestUtils";

vi.mock("../../app/store", () => ({
  useUi: (selector: (state: { pushToast: () => void }) => unknown) =>
    selector({ pushToast: () => undefined }),
}));

import { Markdown, parseBlocks } from "../../features/ai/Markdown";
import {
  highlightSpans,
  normalizeFenceLang,
  peekParser,
  spanClasses,
  tokenizeCode,
} from "../../features/ai/codeHighlight";
import { javascript } from "@codemirror/legacy-modes/mode/javascript";

describe("M152 AI markdown code highlight", () => {
  let view: MountedView | null = null;

  afterEach(() => {
    view?.unmount();
    view = null;
  });

  it("normalizes fence language aliases", () => {
    expect(normalizeFenceLang("TS")).toBe("typescript");
    expect(normalizeFenceLang("py")).toBe("python");
    expect(normalizeFenceLang("Bash")).toBe("bash");
    expect(normalizeFenceLang("c++")).toBe("cpp");
    expect(normalizeFenceLang("yml")).toBe("yaml");
    expect(normalizeFenceLang("")).toBeNull();
    expect(normalizeFenceLang("text")).toBeNull();
    expect(normalizeFenceLang("someunknown")).toBeNull();
  });

  it("tokenizes javascript into styled spans without losing text", () => {
    const code = 'const s = "x"; // note';
    const spans = tokenizeCode(javascript, code);
    const byStyle = (style: string) =>
      spans
        .filter((s) => s.style === style)
        .map((s) => s.text)
        .join("");
    expect(byStyle("keyword")).toContain("const");
    expect(byStyle("string")).toContain('"x"');
    expect(byStyle("comment")).toContain("// note");
    expect(spans.map((s) => s.text).join("")).toBe(code);
  });

  it("maps dotted and dashed style names to token classes", () => {
    expect(spanClasses("variableName.constant")).toBe("nx-tok-variableName nx-tok-constant");
    expect(spanClasses("string-2")).toBe("nx-tok-string-2");
  });

  it("parses an unclosed fence as a code block while streaming", () => {
    expect(parseBlocks("前言\n\n```js\nconst a = 1")).toEqual([
      { kind: "p", text: "前言" },
      { kind: "code", lang: "js", code: "const a = 1" },
    ]);
  });

  it("renders plain text first, then upgrades to spans without changing the text", async () => {
    view = mount(createElement(Markdown, { text: "```js\nconst a = 1\n```" }));
    const body = () => view!.container.querySelector(".nx-md-pre-body")!;
    expect(body().textContent).toBe("const a = 1");
    expect(body().querySelector("[class*='nx-tok-']")).toBeNull();
    await flushUntil(() => body().querySelector("[class*='nx-tok-']") !== null);
    expect(body().textContent).toBe("const a = 1");
    expect(peekParser("javascript")).not.toBeNull();
  });

  it("highlights an unclosed fence while it is still streaming", async () => {
    view = mount(createElement(Markdown, { text: "```python\nprint(\"hi\")\n" }));
    const body = () => view!.container.querySelector(".nx-md-pre-body")!;
    await flushUntil(() => body().querySelector("[class*='nx-tok-']") !== null);
    expect(body().textContent).toBe('print("hi")');
  });

  it("keeps unknown languages plain and keeps the copy button", async () => {
    view = mount(createElement(Markdown, { text: "```someunknown\nplain text\n```" }));
    const pre = view!.container.querySelector(".nx-md-pre")!;
    expect(pre.querySelector(".nx-md-pre-lang")!.textContent).toBe("someunknown");
    expect(pre.querySelector("button[title='复制这段']")).not.toBeNull();
    expect(pre.querySelector(".nx-md-pre-body")!.textContent).toBe("plain text");
    await flush();
    expect(pre.querySelector("[class*='nx-tok-']")).toBeNull();
  });

  it("copies the raw code from the copy button", async () => {
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    view = mount(createElement(Markdown, { text: "```js\nconst a = 1\n```" }));
    const button = view!.container.querySelector<HTMLButtonElement>("button[title='复制这段']")!;
    click(button);
    await flushUntil(() => writeText.mock.calls.length > 0);
    expect(writeText).toHaveBeenCalledWith("const a = 1");
  });

  it("injects the highlight theme with a light override", () => {
    view = mount(createElement(Markdown, { text: "```js\nx\n```" }));
    const style = document.getElementById("nx-md-highlight-theme");
    expect(style).not.toBeNull();
    expect(style!.textContent).toContain(':root[data-nx-theme="light"]');
    expect(style!.textContent).toContain(".nx-tok-keyword");
  });

  it("keeps very large blocks plain instead of stalling the stream", () => {
    const parser = peekParser("javascript");
    expect(parser).not.toBeNull();
    expect(highlightSpans("javascript", "x".repeat(200_001), parser!)).toBeNull();
  });

  it("renders tables and long lines around a highlighted block", async () => {
    const long = "a".repeat(5000);
    const text = ["| a | b |", "| --- | --- |", "| 1 | 2 |", "", "```bash", 'echo "hi"', "```", "", long].join("\n");
    view = mount(createElement(Markdown, { text }));
    const container = view!.container;
    expect(container.querySelectorAll(".nx-md-table tbody tr")).toHaveLength(1);
    await flushUntil(() => container.querySelector(".nx-md-pre-body [class*='nx-tok-']") !== null);
    expect(container.querySelector(".nx-md-pre-body")!.textContent).toBe('echo "hi"');
    expect(container.textContent).toContain(long);
  });
});
