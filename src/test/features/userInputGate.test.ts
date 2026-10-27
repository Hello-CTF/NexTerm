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
  return { gate, textarea };
}

describe("user input gate", () => {
  it("is active while the event's own listeners run and ends in its bubble phase", () => {
    const { gate, textarea } = setup();
    const seen: boolean[] = [];
    textarea.addEventListener("keydown", () => seen.push(gate.active()));
    textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "F3", bubbles: true }));
    expect(seen).toEqual([true]);
    // A parse timer queued before the keystroke runs after it: not user input.
    expect(gate.active()).toBe(false);
  });

  it("survives microtask checkpoints and ends on the next task when propagation stops", async () => {
    const { gate, textarea } = setup();
    textarea.addEventListener("keydown", (event) => event.stopPropagation());
    textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "F3", bubbles: true }));
    await Promise.resolve();
    expect(gate.active()).toBe(true);
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(gate.active()).toBe(false);
  });

  it("covers focus changes through their bubbling twins", () => {
    const { gate, textarea } = setup();
    const seen: boolean[] = [];
    textarea.addEventListener("focus", () => seen.push(gate.active()));
    textarea.focus();
    expect(seen).toEqual([true]);
    expect(gate.active()).toBe(false);
  });
});
