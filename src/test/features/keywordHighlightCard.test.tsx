/** @vitest-environment jsdom */
import { createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { click, clickButton, flush, mountWithClient, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return { assetList: vi.fn() };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, assetApi: { list: mocks.assetList } };
});

import { KeywordHighlightCard } from "../../features/settings/KeywordHighlightCard";
import { registerAccountPreferenceStore } from "../../app/preferences";
import { loadRegexEngine } from "../../features/terminal/keywordHighlightRules";

const STORAGE_KEY = "nexterm.keywordHighlights.v1";
let mounted: MountedView | undefined;

function stored(): { presets: string[]; rules: { pattern: string; enabled: boolean }[] } | null {
  const raw = localStorage.getItem(STORAGE_KEY);
  return raw === null ? null : JSON.parse(raw);
}

function checkbox(label: string): HTMLInputElement {
  const found = [...mounted!.container.querySelectorAll("label")].find((candidate) => candidate.textContent?.includes(label));
  const input = found?.querySelector<HTMLInputElement>('input[type="checkbox"]');
  if (!input) throw new Error(`checkbox ${label} not found`);
  return input;
}

function marks(): string[] {
  return [...mounted!.container.querySelectorAll('[data-testid="keyword-highlight-preview"] mark')].map((mark) => mark.textContent ?? "");
}

function patterns(): string[] {
  return [...mounted!.container.querySelectorAll("[data-rule-id]")].map((row) => row.querySelector(".font-mono")?.textContent ?? "");
}

async function render() {
  await loadRegexEngine();
  mounted = mountWithClient(createElement(KeywordHighlightCard));
  await flush();
}

async function addRule(pattern: string, options: { regex?: boolean } = {}) {
  clickButton(mounted!.container, "新建规则");
  const form = mounted!.container.querySelector<HTMLFormElement>('form[aria-label="新建高亮规则"]')!;
  setInputValue(form.querySelector<HTMLInputElement>("input.nx-input")!, pattern);
  if (options.regex) click(checkbox("正则表达式"));
  clickButton(form, "保存");
  await flush();
}

beforeEach(() => {
  registerAccountPreferenceStore(null);
  localStorage.clear();
  mocks.assetList.mockResolvedValue([]);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  localStorage.clear();
});

describe("KeywordHighlightCard", () => {
  it("starts with every preset off and enables one on request", async () => {
    await render();
    expect(checkbox("错误").checked).toBe(false);
    expect(marks()).toEqual([]);
    click(checkbox("错误"));
    await flush();
    expect(stored()?.presets).toEqual(["errors"]);
    expect(marks()).toEqual(["ERROR", "failed", "denied"]);
  });

  it("adds, reorders and deletes rules and previews them before saving", async () => {
    await render();
    clickButton(mounted!.container, "新建规则");
    const form = mounted!.container.querySelector<HTMLFormElement>('form[aria-label="新建高亮规则"]')!;
    setInputValue(form.querySelector<HTMLInputElement>("input.nx-input")!, "disk");
    expect(marks()).toEqual(["disk"]);
    expect(stored()).toBeNull();
    clickButton(form, "保存");
    await flush();
    await addRule("WARN");
    expect(patterns()).toEqual(["disk", "WARN"]);
    expect(stored()?.rules.map((rule) => rule.pattern)).toEqual(["disk", "WARN"]);

    click(mounted!.container.querySelector('button[aria-label="上移 WARN"]')!);
    await flush();
    expect(patterns()).toEqual(["WARN", "disk"]);

    click(mounted!.container.querySelector('input[aria-label="启用 WARN"]')!);
    await flush();
    expect(stored()?.rules[0].enabled).toBe(false);
    expect(marks()).toEqual(["disk"]);

    click(mounted!.container.querySelector('button[aria-label="删除 disk"]')!);
    await flush();
    expect(patterns()).toEqual(["WARN"]);
  });

  it("refuses a regular expression RE2 does not support", async () => {
    await render();
    await addRule("foo(?=bar)", { regex: true });
    const alert = mounted!.container.querySelector('form [role="alert"]');
    expect(alert?.textContent).toContain("正则表达式无效");
    expect(stored()).toBeNull();
  });

  it("shows a stored rule that no longer validates instead of dropping it", async () => {
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({ presets: [], rules: [{ id: "a", pattern: "(?=x)y", regex: true, foreground: "red", enabled: true }] }),
    );
    await render();
    expect(patterns()).toEqual(["(?=x)y"]);
    expect(mounted!.container.querySelector("[data-rule-id]")?.textContent).toContain("未生效");
  });
});
