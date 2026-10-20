/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { act, createElement } from "react";
import { useSnippetVariablePrompt } from "../../features/explorer/SnippetVariablePrompt";
import type { SnippetVariable, SnippetVariableValues } from "../../features/explorer/snippetVariables";
import { clickButton, flush, mount, setInputValue, type MountedView } from "./reactTestUtils";

interface PromptApi {
  promptForValues: (title: string, variables: readonly SnippetVariable[]) => Promise<SnippetVariableValues | null>;
}

const variables: SnippetVariable[] = [{ name: "who", required: true, multiline: false }];

let api: PromptApi | null = null;
let mounted: MountedView | undefined;

function Harness() {
  const hook = useSnippetVariablePrompt();
  api = hook;
  return <>{hook.promptElement}</>;
}

function dialogText(): string {
  return document.body.textContent ?? "";
}

function input(): HTMLInputElement {
  const field = document.body.querySelector<HTMLInputElement>('input[aria-label="变量 who"]');
  if (!field) throw new Error("prompt input not found");
  return field;
}

beforeEach(() => {
  document.body.replaceChildren();
  api = null;
  mounted = mount(createElement(Harness));
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  api = null;
});

describe("concurrent snippet variable prompts", () => {
  it("resolves overlapping prompts in FIFO order with real typed input", async () => {
    let first!: Promise<SnippetVariableValues | null>;
    let second!: Promise<SnippetVariableValues | null>;
    act(() => {
      first = api!.promptForValues("第一个提示", variables);
      second = api!.promptForValues("第二个提示", variables);
    });
    await flush();

    // Only the first dialog is on screen; the second waits in the queue.
    expect(dialogText()).toContain("第一个提示");
    expect(dialogText()).not.toContain("第二个提示");

    setInputValue(input(), "alice");
    clickButton(document.body, "继续");
    await flush();
    await expect(first).resolves.toEqual({ who: "alice" });

    expect(dialogText()).toContain("第二个提示");
    setInputValue(input(), "bob");
    clickButton(document.body, "继续");
    await flush();
    await expect(second).resolves.toEqual({ who: "bob" });
    expect(dialogText()).not.toContain("第二个提示");
  });

  it("resolves a canceled prompt with null and still shows the queued one", async () => {
    let first!: Promise<SnippetVariableValues | null>;
    let second!: Promise<SnippetVariableValues | null>;
    act(() => {
      first = api!.promptForValues("第一个提示", variables);
      second = api!.promptForValues("第二个提示", variables);
    });
    await flush();

    clickButton(document.body, "取消");
    await flush();
    await expect(first).resolves.toBeNull();

    expect(dialogText()).toContain("第二个提示");
    setInputValue(input(), "carol");
    clickButton(document.body, "继续");
    await expect(second).resolves.toEqual({ who: "carol" });
  });
});
