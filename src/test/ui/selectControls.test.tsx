/** @vitest-environment jsdom */
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, mount, type MountedView } from "../features/reactTestUtils";
import { Combobox } from "../../ui/Combobox";
import { DownloadProgress } from "../../ui/DownloadProgress";
import { Select } from "../../ui/Select";

let mounted: MountedView | undefined;
afterEach(() => { mounted?.unmount(); mounted = undefined; });

describe("select controls", () => {
  it("selects an option with the mouse and exposes listbox semantics", () => {
    const onChange = vi.fn();
    mounted = mount(createElement(Select, {
      ariaLabel: "模型精度", value: "int8", onChange,
      options: [{ value: "int8", label: "INT8" }, { value: "fp32", label: "FP32", description: "精度优先" }],
    }));
    const trigger = mounted.container.querySelector<HTMLButtonElement>('button[aria-label="模型精度"]')!;
    expect(trigger.getAttribute("aria-haspopup")).toBe("listbox");
    click(trigger);
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    const option = [...mounted.container.querySelectorAll<HTMLElement>('[role="option"]')].find((item) => item.textContent?.includes("FP32"));
    click(option!);
    expect(onChange).toHaveBeenCalledWith("fp32");
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
  });

  it("places the list from the trigger, above it when there is no room below", () => {
    mounted = mount(createElement(Select, {
      ariaLabel: "位置", value: "a", onChange: vi.fn(), title: "选择位置",
      options: [{ value: "a", label: "A" }, { value: "b", label: "B" }],
    }));
    const trigger = mounted.container.querySelector<HTMLButtonElement>('button[aria-label="位置"]')!;
    expect(trigger.title).toBe("选择位置");
    const rect = (top: number) => () => ({ top, bottom: top + 30, left: 40, right: 240, width: 200, height: 30, x: 40, y: top, toJSON: () => ({}) }) as DOMRect;
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 600 });

    trigger.getBoundingClientRect = rect(100);
    click(trigger);
    const below = mounted.container.querySelector<HTMLElement>('[role="listbox"]')!;
    expect(below.style.position === "" || below.style.position === "fixed").toBe(true);
    expect(below.style.top).toBe("134px");
    expect(below.style.bottom).toBe("");
    click(trigger);

    trigger.getBoundingClientRect = rect(560);
    click(trigger);
    const above = mounted.container.querySelector<HTMLElement>('[role="listbox"]')!;
    expect(above.style.bottom).toBe("44px");
    expect(above.style.top).toBe("");
  });

  it("closes when the page scrolls outside the list", () => {
    mounted = mount(createElement(Select, {
      ariaLabel: "滚动", value: "a", onChange: vi.fn(),
      options: [{ value: "a", label: "A" }, { value: "b", label: "B" }],
    }));
    const trigger = mounted.container.querySelector<HTMLButtonElement>('button[aria-label="滚动"]')!;
    click(trigger);
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    act(() => { mounted!.container.querySelector('[role="listbox"]')!.dispatchEvent(new Event("scroll", { bubbles: true })); });
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    act(() => { document.body.dispatchEvent(new Event("scroll", { bubbles: true })); });
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
  });

  it("supports keyboard navigation without moving focus into the popup", () => {
    const onChange = vi.fn();
    mounted = mount(createElement(Select, {
      ariaLabel: "模型精度", value: "int8", onChange,
      options: [{ value: "int8", label: "INT8" }, { value: "fp32", label: "FP32" }],
    }));
    const trigger = mounted.container.querySelector<HTMLButtonElement>('button[aria-label="模型精度"]')!;
    act(() => {
      trigger.focus();
      trigger.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
    });
    expect(document.activeElement).toBe(trigger);
    expect(trigger.getAttribute("aria-activedescendant")).toBeTruthy();
    act(() => { trigger.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true })); });
    act(() => { trigger.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })); });
    expect(onChange).toHaveBeenCalledWith("fp32");
  });

  it("keeps combobox input editable while offering styled presets", () => {
    const onChange = vi.fn();
    mounted = mount(createElement(Combobox, {
      ariaLabel: "思考强度", value: "high", onChange,
      options: [{ value: "low" }, { value: "medium" }, { value: "high" }],
    }));
    const input = mounted.container.querySelector<HTMLInputElement>('input[role="combobox"]')!;
    act(() => { input.focus(); });
    expect(input.getAttribute("aria-expanded")).toBe("true");
    const option = [...mounted.container.querySelectorAll<HTMLElement>('[role="option"]')].find((item) => item.textContent === "medium");
    click(option!);
    expect(onChange).toHaveBeenCalledWith("medium");
  });

  it("reports determinate download progress and formatted sizes", () => {
    mounted = mount(createElement(DownloadProgress, { label: "正在下载 INT8", transferred: 512 * 1024, total: 1024 * 1024 }));
    const progress = mounted.container.querySelector('[role="progressbar"]')!;
    expect(progress.getAttribute("aria-valuenow")).toBe("50");
    expect(mounted.container.textContent).toContain("50% · 512 KiB / 1.0 MiB");
  });
});
