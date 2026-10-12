import { act, createElement, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { vi } from "vitest";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

export interface MountedView {
  container: HTMLDivElement;
  root: Root;
  unmount: () => void;
}

export function mount(node: ReactNode): MountedView {
  const container = document.createElement("div");
  document.body.append(container);
  const root = createRoot(container);
  act(() => root.render(node));
  return {
    container,
    root,
    unmount: () => {
      act(() => root.unmount());
      container.remove();
    },
  };
}

// mountWithClient 供依赖 react-query 的视图使用 (如桌面账号身份查询);
// retry:false 让查询错误立刻落定, 不拖慢断言。
export function mountWithClient(node: ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

export async function flush(): Promise<void> {
  await act(async () => {
    const macrotask = new Promise<void>((resolve) => {
      setTimeout(resolve, 0);
    });
    if (vi.isFakeTimers()) {
      await vi.advanceTimersByTimeAsync(0);
    }
    await macrotask;
  });
}

export async function waitFor(assertion: () => void): Promise<void> {
  await act(async () => {
    await vi.waitFor(assertion);
  });
}

export async function flushUntil(predicate: () => boolean, maxTicks = 60): Promise<void> {
  for (let i = 0; i < maxTicks; i++) {
    if (predicate()) return;
    await flush();
  }
  if (!predicate()) throw new Error("flushUntil: condition not met");
}

export function click(element: Element): void {
  act(() => {
    element.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

export function clickButton(container: ParentNode, text: string): HTMLButtonElement {
  const button = [...container.querySelectorAll("button")].find(
    (candidate) => candidate.textContent?.trim() === text,
  );
  if (!button) throw new Error(`Button not found: ${text}`);
  click(button);
  return button;
}

export function setInputValue(input: HTMLInputElement | HTMLTextAreaElement, value: string): void {
  const prototype = input instanceof HTMLTextAreaElement ? HTMLTextAreaElement : HTMLInputElement;
  const setter = Object.getOwnPropertyDescriptor(prototype.prototype, "value")?.set;
  if (!setter) throw new Error("Native input value setter unavailable");
  act(() => {
    setter.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

export function setSelectValue(select: HTMLSelectElement, value: string): void {
  const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")?.set;
  if (!setter) throw new Error("Native select value setter unavailable");
  act(() => {
    setter.call(select, value);
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

export function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}
