/** @vitest-environment jsdom */

import { afterEach, describe, expect, it } from "vitest";
import { configureImeCommitEnter, isImeEnter } from "../../ui/imeEnter";

let now = 0;

function field(): { input: HTMLInputElement; enter: (init?: KeyboardEventInit) => boolean; key: (key: string) => void } {
  const input = document.createElement("input");
  document.body.append(input);
  let result = false;
  input.addEventListener("keydown", (event) => {
    result = isImeEnter(event);
  });
  return {
    input,
    enter: (init = {}) => {
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, ...init }));
      return result;
    },
    key: (key) => {
      input.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
    },
  };
}

function compositionEnd(target: EventTarget) {
  target.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true }));
}

afterEach(() => {
  configureImeCommitEnter({ webkit: null, now: () => performance.now() });
  document.body.replaceChildren();
});

describe("isImeEnter", () => {
  it("treats a composing Enter and keyCode 229 as input method keys", () => {
    configureImeCommitEnter({ webkit: false });
    const { enter } = field();
    expect(enter({ isComposing: true })).toBe(true);
    expect(enter({ keyCode: 229 } as KeyboardEventInit)).toBe(true);
    expect(enter()).toBe(false);
  });

  it("on WebKit swallows only the first Enter within the commit window after compositionend in the same field", () => {
    now = 0;
    configureImeCommitEnter({ webkit: true, now: () => now });
    const { input, enter, key } = field();

    compositionEnd(input);
    now += 10;
    expect(enter()).toBe(true);
    expect(enter()).toBe(false);

    compositionEnd(input);
    now += 51;
    expect(enter()).toBe(false);

    // Another key in between ends the window: the following Enter is the user's.
    compositionEnd(input);
    key("a");
    expect(enter()).toBe(false);

    // A composition that ended in another field does not hold back this field's Enter.
    const other = field();
    compositionEnd(other.input);
    expect(enter()).toBe(false);
  });

  it("keeps a real Enter right after compositionend on engines that report the committing Enter themselves", () => {
    configureImeCommitEnter({ webkit: false, now: () => 0 });
    const { input, enter } = field();
    compositionEnd(input);
    expect(enter()).toBe(false);
  });
});
