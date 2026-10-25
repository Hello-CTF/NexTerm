/** @vitest-environment jsdom */
// Shared compare row component: summary stats, status badges (including the desktop-only "undecryptable"),
// collapse interaction, read placeholders and hidden errors, empty-state text, no type badge rendered when kind is missing.
import { afterEach, describe, expect, it } from "vitest";
import { createElement } from "react";
import { clickButton, flush, mount, type MountedView } from "./features/reactTestUtils";
import { CompareDetails, compareRowTitle, compareSummary, type CompareRowView } from "../features/settings/syncCompareRows";

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

describe("compareSummary summary statistics", () => {
  it("covers loading, empty, different, and fully synchronized states", () => {
    expect(compareSummary(null)).toBe("读取中…");
    expect(compareSummary([])).toBe("本机与服务端都还没有可同步的内容");
    expect(compareSummary(ROWS)).toBe("资产 1 · 分组 1 · 凭据 1 · 无法识别的对象 1 — 差异 3 条");
    expect(compareSummary([{ id: "x", kind: "asset", name: "a", state: "same" }])).toBe("资产 1 — 全部一致");
  });

  it("groups matching rows by category and hides internal tombstones when they match", () => {
    const rows: CompareRowView[] = [
      { id: "a1", kind: "asset", name: "web-01", state: "same" },
      { id: "a2", kind: "asset", name: "web-02", state: "same" },
      { id: "h1", kind: "known_host", name: "10.0.0.1:22", state: "same" },
      { id: "t1", kind: "tombstone", name: "01M4ZHMP33G12RV4KW43GPZZA1", state: "same" },
    ];
    const summary = compareSummary(rows);
    expect(summary).toBe("资产 2 · 已知主机 1 — 全部一致");
    expect(summary).not.toContain("删除标记");
  });

  it("describes a differing tombstone plainly", () => {
    const summary = compareSummary([{ id: "t1", kind: "tombstone", name: "t1", state: "remote-deleted" }]);
    expect(summary).toBe("已删除的项目 1 — 差异 1 条");
  });
});

describe("compareRowTitle", () => {
  it("demotes ids and internal keys to secondary text", () => {
    expect(compareRowTitle({ id: "a", kind: "asset", name: "web-01", state: "same" })).toEqual({ title: "web-01" });
    expect(compareRowTitle({ id: "01J0NEXTERMLOCALDEVICE0001", kind: "ai_grant", name: "01J0NEXTERMLOCALDEVICE0001 exec_commands", state: "local-only" }))
      .toEqual({ title: "AI 授权", secondary: "01J0NEXTERMLOCALDEVICE0001 exec_commands" });
    expect(compareRowTitle({ id: "01M4ZHMP33G12RV4KW43GPZZA1", kind: "tombstone", name: "01M4ZHMP33G12RV4KW43GPZZA1", state: "remote-deleted" }))
      .toEqual({ title: "已删除的项目", secondary: "01M4ZHMP33G12RV4KW43GPZZA1" });
    expect(compareRowTitle({ id: "01ARZ3NDEKTSV4RRFFQ69G5FAV", kind: "", name: "01ARZ3NDEKTSV4RRFFQ69G5FAV", state: "remote-undecryptable" }))
      .toEqual({ title: "无法识别的对象", secondary: "01ARZ3NDEKTSV4RRFFQ69G5FAV" });
    expect(compareRowTitle({ id: "k1", kind: "known_host", name: "103.164.32.105:10022", state: "local-only" }))
      .toEqual({ title: "103.164.32.105:10022" });
  });
});

describe("CompareDetails row rendering", () => {
  it("shows only the summary when collapsed and lists only differing rows after expanding", async () => {
    mounted = mount(createElement(CompareDetails, { rows: ROWS }));
    expect(text()).toContain("差异 3 条");
    expect(text()).not.toContain("web-01");

    clickButton(mounted.container, "对比详情");
    await flush();
    expect(text()).toContain("web-01");
    expect(text()).toContain("服务端较新");
    expect(text()).toContain("生产环境");
    expect(text()).toContain("仅本机");
    expect(text()).toContain("无法解密");
    expect(text()).not.toContain("root 密码");
    expect(text()).not.toContain("已一致");
    expect(text()).toContain("资产");
    expect(text()).toContain("分组");
  });

  it("lists no rows when everything matches", async () => {
    const rows: CompareRowView[] = [
      { id: "a1", kind: "asset", name: "web-01", state: "same" },
      { id: "t1", kind: "tombstone", name: "01M4ZHMP33G12RV4KW43GPZZA1", state: "same" },
    ];
    mounted = mount(createElement(CompareDetails, { rows }));
    clickButton(mounted.container, "对比详情");
    await flush();
    expect(text()).toContain("资产 1 — 全部一致");
    expect(text()).toContain("全部一致，无需处理");
    expect(text()).not.toContain("web-01");
    expect(text()).not.toContain("01M4ZHMP33G12RV4KW43GPZZA1");
    expect(mounted.container.querySelectorAll("div.border-b").length).toBe(0);
  });

  it("shows ids only as secondary text for differing internal objects", async () => {
    const rows: CompareRowView[] = [
      { id: "01M4ZHMP33G12RV4KW43GPZZA1", kind: "tombstone", name: "01M4ZHMP33G12RV4KW43GPZZA1", state: "remote-deleted" },
    ];
    mounted = mount(createElement(CompareDetails, { rows }));
    clickButton(mounted.container, "对比详情");
    await flush();
    const row = mounted.container.querySelector<HTMLElement>("div.border-b");
    expect(row?.firstElementChild?.firstChild?.textContent).toBe("已删除的项目");
    expect(row?.textContent).toContain("01M4ZHMP33G12RV4KW43GPZZA1");
  });

  it("does not render a type badge for rows with an empty kind (undecryptable cloud objects)", async () => {
    mounted = mount(createElement(CompareDetails, { rows: ROWS }));
    clickButton(mounted.container, "对比详情");
    await flush();
    const undecryptable = [...mounted.container.querySelectorAll<HTMLElement>("div.border-b")].find((d) =>
      d.textContent?.includes("01ARZ3NDEKTSV4RRFFQ69G5FAV"),
    );
    if (!undecryptable) throw new Error("undecryptable row not found");
    expect(undecryptable.textContent).not.toContain("资产");
    expect(undecryptable.title).toContain("密钥不匹配");
  });

  it("shows a loading placeholder for null rows and hides it on error", async () => {
    mounted = mount(createElement(CompareDetails, { rows: null }));
    expect(text()).toContain("读取中…");
    clickButton(mounted.container, "对比详情");
    await flush();
    expect(text()).toContain("正在读取本机与服务端数据…");

    mounted.unmount();
    mounted = mount(createElement(CompareDetails, { rows: null, error: true }));
    clickButton(mounted.container, "对比详情");
    await flush();
    expect(text()).not.toContain("正在读取本机与服务端数据…");
  });

  it("shows the empty-state message after expanding an empty row array", async () => {
    mounted = mount(createElement(CompareDetails, { rows: [] }));
    clickButton(mounted.container, "对比详情");
    await flush();
    expect(text()).toContain("本机与服务端都还没有可同步的内容。");
  });
});
