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

// mountWithClient is for views that depend on react-query (e.g. the desktop account identity query);
// retry:false makes query errors settle immediately, not slowing assertions.
export function mountWithClient(node: ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

export async function flushMacrotask(): Promise<void> {
  const macrotask = new Promise<void>((resolve) => {
    setTimeout(resolve, 0);
  });
  if (vi.isFakeTimers()) {
    await vi.advanceTimersByTimeAsync(0);
  }
  await macrotask;
}

export async function flush(): Promise<void> {
  await act(async () => {
    await flushMacrotask();
  });
}

export async function waitFor(assertion: () => void, timeout = 1000): Promise<void> {
  await act(async () => {
    await vi.waitFor(assertion, { timeout });
  });
}

// waitFor polls inside a single act scope, so React cannot commit pending updates (lazy panels, deferred state)
// between attempts. waitForRendered flushes a macrotask per attempt, outside any shared act scope, so each retry
// observes a rendered tree. Prefer it for assertions on UI that appears after asynchronous work.
export async function waitForRendered(assertion: () => void, timeout = 1000): Promise<void> {
  const deadline = Date.now() + timeout;
  // Fake timers freeze Date.now, so the tick budget bounds the loop there.
  for (let tick = 0; ; tick++) {
    try {
      assertion();
      return;
    } catch (error) {
      if (Date.now() >= deadline || tick >= timeout) throw error;
    }
    await flush();
  }
}

// flushUntil counts macrotask ticks, so it only bounds work driven by mocks and timers. Real asynchronous work,
// such as the first dynamic import of a React.lazy panel, takes wall-clock time that machine load stretches past
// any tick budget: warm such modules in beforeAll instead.
export async function flushUntil(predicate: () => boolean, maxTicks = 60): Promise<void> {
  for (let i = 0; i < maxTicks; i++) {
    if (predicate()) return;
    await flush();
  }
  if (!predicate()) throw new Error(`flushUntil: condition not met after ${maxTicks} ticks`);
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
