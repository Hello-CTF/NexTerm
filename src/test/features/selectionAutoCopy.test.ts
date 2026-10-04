/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createSelectionAutoCopy, type SelectionAutoCopyOptions } from "../../features/terminal/selectionAutoCopy";

interface Harness {
  selection: string;
  enabled: boolean;
  copies: string[];
  writes: string[];
  errors: unknown[];
  writeError: unknown | null;
}

function makeHarness(overrides: Partial<SelectionAutoCopyOptions> = {}) {
  const harness: Harness = {
    selection: "",
    enabled: true,
    copies: [],
    writes: [],
    errors: [],
    writeError: null,
  };
  const options: SelectionAutoCopyOptions = {
    isEnabled: () => harness.enabled,
    getSelection: () => harness.selection,
    onCopied: (text) => harness.copies.push(text),
    onError: (error) => harness.errors.push(error),
    writeText: (text) => {
      harness.writes.push(text);
      return harness.writeError ? Promise.reject(harness.writeError) : Promise.resolve();
    },
    delayMs: 10,
    ...overrides,
  };
  return { harness, options };
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("createSelectionAutoCopy", () => {
  it("copies the selection after the debounce delay", async () => {
    const { harness, options } = makeHarness();
    const autoCopy = createSelectionAutoCopy(options);
    harness.selection = "hello";
    autoCopy.notifySelectionChanged();
    expect(harness.copies).toEqual([]);
    await vi.advanceTimersByTimeAsync(20);
    expect(harness.copies).toEqual(["hello"]);
    autoCopy.dispose();
  });

  it("debounces rapid selection changes into one copy", async () => {
    const { harness, options } = makeHarness();
    const autoCopy = createSelectionAutoCopy(options);
    for (const text of ["h", "he", "hel", "hello"]) {
      harness.selection = text;
      autoCopy.notifySelectionChanged();
      await vi.advanceTimersByTimeAsync(5);
    }
    await vi.advanceTimersByTimeAsync(20);
    expect(harness.copies).toEqual(["hello"]);
    autoCopy.dispose();
  });

  it("does nothing while the preference is disabled", async () => {
    const { harness, options } = makeHarness();
    harness.enabled = false;
    const autoCopy = createSelectionAutoCopy(options);
    harness.selection = "hello";
    autoCopy.notifySelectionChanged();
    await vi.advanceTimersByTimeAsync(30);
    expect(harness.copies).toEqual([]);
    autoCopy.dispose();
  });

  it("skips empty selections and resets the dedup memory", async () => {
    const { harness, options } = makeHarness();
    const autoCopy = createSelectionAutoCopy(options);
    harness.selection = "same";
    autoCopy.notifySelectionChanged();
    await vi.advanceTimersByTimeAsync(20);
    harness.selection = "";
    autoCopy.notifySelectionChanged();
    await vi.advanceTimersByTimeAsync(20);
    harness.selection = "same";
    autoCopy.notifySelectionChanged();
    await vi.advanceTimersByTimeAsync(20);
    expect(harness.copies).toEqual(["same", "same"]);
    autoCopy.dispose();
  });

  it("does not recopy an unchanged selection", async () => {
    const { harness, options } = makeHarness();
    const autoCopy = createSelectionAutoCopy(options);
    harness.selection = "stable";
    autoCopy.notifySelectionChanged();
    await vi.advanceTimersByTimeAsync(20);
    autoCopy.notifySelectionChanged();
    await vi.advanceTimersByTimeAsync(20);
    expect(harness.copies).toEqual(["stable"]);
    autoCopy.dispose();
  });

  it("reports failures without marking the text as copied", async () => {
    const { harness, options } = makeHarness();
    harness.writeError = new DOMException("denied", "NotAllowedError");
    const autoCopy = createSelectionAutoCopy(options);
    harness.selection = "text";
    autoCopy.notifySelectionChanged();
    await vi.advanceTimersByTimeAsync(20);
    expect(harness.copies).toEqual([]);
    expect(harness.errors).toHaveLength(1);
    harness.writeError = null;
    autoCopy.notifySelectionChanged();
    await vi.advanceTimersByTimeAsync(20);
    expect(harness.copies).toEqual(["text"]);
    autoCopy.dispose();
  });

  it("reports an honest error when the clipboard API is unavailable", async () => {
    const { harness, options } = makeHarness({ writeText: undefined });
    const original = navigator.clipboard;
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
    try {
      const autoCopy = createSelectionAutoCopy(options);
      harness.selection = "text";
      autoCopy.notifySelectionChanged();
      await vi.advanceTimersByTimeAsync(20);
      expect(harness.errors).toHaveLength(1);
      expect(String((harness.errors[0] as Error).message)).toContain("剪贴板");
      autoCopy.dispose();
    } finally {
      Object.defineProperty(navigator, "clipboard", { value: original, configurable: true });
    }
  });

  it("dispose cancels a pending copy", async () => {
    const { harness, options } = makeHarness();
    const autoCopy = createSelectionAutoCopy(options);
    harness.selection = "hello";
    autoCopy.notifySelectionChanged();
    autoCopy.dispose();
    await vi.advanceTimersByTimeAsync(30);
    expect(harness.copies).toEqual([]);
  });
});
