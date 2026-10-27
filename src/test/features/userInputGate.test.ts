/** @vitest-environment jsdom */

import { afterEach, describe, expect, it } from "vitest";
import { createUserInputGate } from "../../features/terminal/userInputGate";

let cleanup: (() => void) | null = null;

afterEach(() => {
  cleanup?.();
  cleanup = null;
});

function setup() {
  const host = document.createElement("div");
  const textarea = document.createElement("textarea");
  host.appendChild(textarea);
  document.body.appendChild(host);
  const gate = createUserInputGate(host);
  cleanup = () => {
    gate.dispose();
    host.remove();
  };
  return { gate, host, textarea };
}

describe("user input gate", () => {
  it("is active while the target's own listeners run and ends right after them", () => {
    const { gate, textarea } = setup();
    const seen: boolean[] = [];
    textarea.addEventListener("keydown", () => seen.push(gate.active()));
    textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "F3", bubbles: true }));
    expect(seen).toEqual([true]);
    // A parse timer queued before the keystroke runs after it: not user input.
    expect(gate.active()).toBe(false);
  });

  it("ends right after a target-phase handler that stops propagation", () => {
    const { gate, textarea } = setup();
    const seen: boolean[] = [];
    textarea.addEventListener("keydown", (event) => {
      event.stopPropagation();
      seen.push(gate.active());
    });
    textarea.addEventListener("keydown", () => seen.push(gate.active()));
    textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "F3", bubbles: true }));
    expect(seen).toEqual([true, true]);
    expect(gate.active()).toBe(false);
  });

  it("falls back to the next task when no listener after the target's can run", async () => {
    const { gate, host } = setup();
    host.dispatchEvent(new KeyboardEvent("keydown", { key: "F3", bubbles: true }));
    await Promise.resolve();
    expect(gate.active()).toBe(true);
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(gate.active()).toBe(false);
  });

  it("covers focus changes", () => {
    const { gate, textarea } = setup();
    const seen: boolean[] = [];
    textarea.addEventListener("focus", () => seen.push(gate.active()));
    textarea.focus();
    expect(seen).toEqual([true]);
    expect(gate.active()).toBe(false);
  });
});
