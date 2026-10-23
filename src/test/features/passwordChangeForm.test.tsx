/** @vitest-environment jsdom */
import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flush, mount, setInputValue, type MountedView } from "./reactTestUtils";
import { PasswordChangeForm } from "../../ui/PasswordChangeForm";
import { buttonWithText, passwordField } from "./passwordFormTestUtils";

let view: MountedView | undefined;

afterEach(() => {
  view?.unmount();
  view = undefined;
});

function check(label: string): string | null | undefined {
  return view!.container.querySelector(`li[aria-label^="${label}"]`)?.getAttribute("aria-label");
}

describe("PasswordChangeForm", () => {
  it("turns each rule green as it holds and only then enables submit", async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    view = mount(createElement(PasswordChangeForm, { requireCurrent: true, submitLabel: "确认修改", onSubmit, onCancel: vi.fn() }));
    const submit = () => buttonWithText(view!.container, "确认修改")!;
    expect(submit().disabled).toBe(true);
    expect(check("至少 8 位")).toContain("未满足");

    setInputValue(passwordField(view.container, "当前密码")!, "old-pass");
    setInputValue(passwordField(view.container, "新密码")!, "short");
    await flush();
    expect(check("至少 8 位")).toContain("未满足");

    setInputValue(passwordField(view.container, "新密码")!, "long-enough-1");
    setInputValue(passwordField(view.container, "确认新密码")!, "long-enough-2");
    await flush();
    expect(check("至少 8 位")).toContain("已满足");
    expect(check("两次输入不一致")).toContain("未满足");
    expect(passwordField(view.container, "确认新密码")!.getAttribute("aria-invalid")).toBe("true");
    expect(submit().disabled).toBe(true);

    setInputValue(passwordField(view.container, "确认新密码")!, "long-enough-1");
    await flush();
    expect(check("两次输入一致")).toContain("已满足");
    expect(submit().disabled).toBe(false);
    click(submit());
    await flush();
    expect(onSubmit).toHaveBeenCalledWith("old-pass", "long-enough-1");
  });

  it("shows a thrown error inline and keeps the form", async () => {
    const onSubmit = vi.fn().mockRejectedValue(new Error("当前密码不正确"));
    view = mount(createElement(PasswordChangeForm, { requireCurrent: true, submitLabel: "确认修改", onSubmit, onCancel: vi.fn() }));
    setInputValue(passwordField(view.container, "当前密码")!, "wrong");
    setInputValue(passwordField(view.container, "新密码")!, "long-enough-1");
    setInputValue(passwordField(view.container, "确认新密码")!, "long-enough-1");
    await flush();
    click(buttonWithText(view.container, "确认修改")!);
    await flush();
    await flush();
    expect(view.container.querySelector('[role="alert"]')?.textContent).toContain("当前密码不正确");
  });

  it("reveals both new fields with one toggle", async () => {
    view = mount(createElement(PasswordChangeForm, { requireCurrent: false, submitLabel: "设置密码", onSubmit: vi.fn(), onCancel: vi.fn() }));
    expect(passwordField(view.container, "当前密码")).toBeNull();
    expect(passwordField(view.container, "新密码")!.type).toBe("password");
    click(view.container.querySelector<HTMLButtonElement>('button[aria-label="显示密码"]')!);
    await flush();
    expect(passwordField(view.container, "新密码")!.type).toBe("text");
    expect(passwordField(view.container, "确认新密码")!.type).toBe("text");
  });
});
