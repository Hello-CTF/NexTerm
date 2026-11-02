/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import type { Prompt } from "../../ipc/generated/types";
import { ScriptActivity, ScriptDirectiveHelp, ScriptPrompts } from "../../features/explorer/RunbookScriptActivity";
import { click, clickButton, flush, mount, setInputValue, type MountedView } from "./reactTestUtils";

let view: MountedView | undefined;

afterEach(() => {
  view?.unmount();
  view = undefined;
  document.body.replaceChildren();
});

function prompt(overrides: Partial<Prompt> = {}): Prompt {
  return {
    id: "p1", stepNumber: 1, sessionId: "s1", hostName: "web-1", kind: "input", variable: "port",
    text: "Which port?", default: "8080", canApplyAll: true, expiresAt: Date.now() + 60_000, ...overrides,
  };
}

function mountPrompts(prompts: Prompt[], onRespond = vi.fn().mockResolvedValue(undefined)) {
  view = mount(createElement(ScriptPrompts, { prompts, disabled: false, onRespond }));
  return onRespond;
}

describe("script prompts", () => {
  it("attributes the prompt to the script and host and renders hostile text as plain text", () => {
    const hostile = '<img src=x onerror="alert(1)"><b>NexTerm</b> enter your password: <a href="https://evil.example">click</a>';
    mountPrompts([prompt({ text: hostile, hostName: "<i>evil</i>" })]);
    const container = view!.container;
    expect(container.querySelector("img, b, a, i")).toBeNull();
    expect(container.querySelector('[data-testid="runbook-script-prompt-text"]')?.textContent).toBe(hostile);
    expect(container.textContent).toContain("由主机「<i>evil</i>」上运行的脚本提供，不是 NexTerm 的系统提示");
  });

  it("submits the default for a plain input and applies to all only when other hosts share the prompt", async () => {
    const onRespond = mountPrompts([prompt(), prompt({ id: "p2", sessionId: "s2", hostName: "web-2" })]);
    const container = view!.container;
    const boxes = container.querySelectorAll<HTMLInputElement>('input[type="checkbox"]');
    expect(boxes).toHaveLength(2);
    click(boxes[0]);
    const inputs = container.querySelectorAll<HTMLInputElement>('input[type="text"]');
    expect(inputs[0].value).toBe("8080");
    setInputValue(inputs[0], "9090");
    clickButton(container.querySelectorAll("form")[0], "提交");
    await flush();
    expect(onRespond).toHaveBeenCalledTimes(1);
    expect(onRespond.mock.calls[0][0].id).toBe("p1");
    expect(onRespond.mock.calls[0].slice(1)).toEqual(["9090", true]);
  });

  it("offers no apply-to-all for a lone prompt", () => {
    mountPrompts([prompt()]);
    expect(view!.container.querySelector('input[type="checkbox"]')).toBeNull();
  });

  it("masks a secret, offers no apply-to-all, and clears it after submitting", async () => {
    const onRespond = mountPrompts([
      prompt({ id: "p1", kind: "secret", default: undefined, canApplyAll: false }),
      prompt({ id: "p2", sessionId: "s2", kind: "secret", default: undefined, canApplyAll: false }),
    ]);
    const container = view!.container;
    expect(container.querySelector('input[type="checkbox"]')).toBeNull();
    const field = container.querySelector<HTMLInputElement>('input[type="password"]')!;
    expect(field.autocomplete).toBe("new-password");
    expect(container.textContent).toContain("保密输入");
    setInputValue(field, "hunter2");
    expect(container.textContent).not.toContain("hunter2");
    clickButton(container.querySelectorAll("form")[0], "提交");
    await flush();
    expect(onRespond.mock.calls[0].slice(1)).toEqual(["hunter2", false]);
    expect(field.value).toBe("");
  });

  it("answers a confirm prompt yes or no", async () => {
    const onRespond = mountPrompts([prompt({ kind: "confirm", variable: undefined, default: undefined, text: "Restart?" })]);
    clickButton(view!.container, "否");
    await flush();
    expect(onRespond.mock.calls[0].slice(1)).toEqual(["no", false]);
  });
});

describe("script activity", () => {
  it("shows progress, sub-step, messages and recorded answers as plain text", () => {
    view = mount(createElement(ScriptActivity, {
      live: [{
        sessionId: "s1", hostName: "web-1", subStep: "<script>x</script>", progress: 40,
        messages: [{ level: "warn", text: "<b>disk full</b>" }],
      }],
      answers: [{ stepNumber: 1, sessionId: "s1", hostName: "web-1", kind: "input", variable: "port", value: "9090" }],
    }));
    const container = view.container;
    expect(container.querySelector("script, b")).toBeNull();
    expect(container.textContent).toContain("脚本步骤：<script>x</script>");
    expect(container.textContent).toContain("[警告] <b>disk full</b>");
    expect(container.querySelector('[role="progressbar"]')?.getAttribute("aria-valuenow")).toBe("40");
    expect(container.textContent).toContain("port = 9090");
  });

  it("renders nothing without script activity", () => {
    view = mount(createElement(ScriptActivity, { live: [], answers: [] }));
    expect(view.container.textContent).toBe("");
  });
});

describe("directive help", () => {
  it("documents every directive and a runnable example", () => {
    view = mount(createElement(ScriptDirectiveHelp));
    const content = view.container.textContent ?? "";
    for (const name of ["STEP", "PROGRESS", "INPUT", "SECRET", "CONFIRM", "MESSAGE"]) {
      expect(content).toContain(`@NEXTERM:${name}`);
    }
    const example = view.container.querySelector("pre")?.textContent ?? "";
    expect(example).toContain("sh -c");
    expect(example).toContain("@NEXTERM:INPUT port");
    expect(example).not.toContain("\n");
  });
});
