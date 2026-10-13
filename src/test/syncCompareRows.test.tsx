/** @vitest-environment jsdom */
// Shared compare row component: summary stats, status badges (including the desktop-only "undecryptable"),
// collapse interaction, read placeholders and hidden errors, empty-state text, no type badge rendered when kind is missing.
import { afterEach, describe, expect, it } from "vitest";
import { createElement } from "react";
import { clickButton, flush, mount, type MountedView } from "./features/reactTestUtils";
import { CompareDetails, compareSummary, type CompareRowView } from "../features/settings/syncCompareRows";

let mounted: MountedView | undefined;

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  document.body.replaceChildren();
});

const ROWS: CompareRowView[] = [
  { id: "a1", kind: "asset", name: "web-01", state: "remote-newer" },
  { id: "g1", kind: "group", name: "生产环境", state: "local-only" },
  { id: "c1", kind: "credential", name: "root 密码", state: "same" },
  { id: "u1", kind: "", name: "01ARZ3NDEKTSV4RRFFQ69G5FAV", state: "remote-undecryptable" },
];

function text(): string {
  return mounted?.container.textContent ?? "";
}

describe("compareSummary 汇总统计", () => {
  it("覆盖读取中/空态/有差异/全一致四种形态", () => {
    expect(compareSummary(null)).toBe("读取中…");
    expect(compareSummary([])).toBe("本机与云端都还没有可同步的内容");
    expect(compareSummary(ROWS)).toBe("共 4 条 · 一致 1 条 · 差异 3 条");
    expect(compareSummary([{ id: "x", kind: "asset", name: "a", state: "same" }])).toBe("共 1 条 · 一致 1 条");
  });
});

describe("CompareDetails 行渲染", () => {
  it("折叠时只见汇总, 展开后逐行渲染名称/类型/状态徽章", async () => {
    mounted = mount(createElement(CompareDetails, { rows: ROWS }));
    expect(text()).toContain("共 4 条 · 一致 1 条 · 差异 3 条");
    expect(text()).not.toContain("web-01");

    clickButton(mounted.container, "对比详情");
    await flush();
    expect(text()).toContain("web-01");
    expect(text()).toContain("云端较新");
    expect(text()).toContain("生产环境");
    expect(text()).toContain("仅本机");
    expect(text()).toContain("root 密码");
    expect(text()).toContain("已一致");
    expect(text()).toContain("无法解密");
    expect(text()).toContain("资产");
    expect(text()).toContain("分组");
    expect(text()).toContain("凭据");
  });

  it("kind 为空的行(云端不可解对象)不渲染类型标", async () => {
    mounted = mount(createElement(CompareDetails, { rows: ROWS }));
    clickButton(mounted.container, "对比详情");
    await flush();
    const undecryptable = [...mounted.container.querySelectorAll<HTMLElement>("div.border-b")].find((d) =>
      d.textContent?.includes("01ARZ3NDEKTSV4RRFFQ69G5FAV"),
    );
    if (!undecryptable) throw new Error("undecryptable row not found");
    // Only the name and badge spans; a normal row also has a third span for the type badge.
    expect(undecryptable.querySelectorAll("span").length).toBe(2);
    const normal = [...mounted.container.querySelectorAll<HTMLElement>("div.border-b")].find((d) =>
      d.textContent?.includes("web-01"),
    );
    if (!normal) throw new Error("normal row not found");
    expect(normal.querySelectorAll("span").length).toBe(3);
  });

  it("行徽章附带判定说明(title)", async () => {
    mounted = mount(createElement(CompareDetails, { rows: ROWS }));
    clickButton(mounted.container, "对比详情");
    await flush();
    const row = [...mounted.container.querySelectorAll<HTMLElement>("div.border-b")].find((d) =>
      d.textContent?.includes("01ARZ3NDEKTSV4RRFFQ69G5FAV"),
    );
    expect(row?.title).toContain("密钥不匹配");
  });

  it("rows 为 null 显示读取占位, error 时隐藏占位", async () => {
    mounted = mount(createElement(CompareDetails, { rows: null }));
    expect(text()).toContain("读取中…");
    clickButton(mounted.container, "对比详情");
    await flush();
    expect(text()).toContain("正在读取本机与云端数据…");

    mounted.unmount();
    mounted = mount(createElement(CompareDetails, { rows: null, error: true }));
    clickButton(mounted.container, "对比详情");
    await flush();
    expect(text()).not.toContain("正在读取本机与云端数据…");
  });

  it("空行数组展开后显示空态文案", async () => {
    mounted = mount(createElement(CompareDetails, { rows: [] }));
    clickButton(mounted.container, "对比详情");
    await flush();
    expect(text()).toContain("本机与云端都还没有可同步的内容。");
  });
});
