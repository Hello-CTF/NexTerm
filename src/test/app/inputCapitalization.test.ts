/** @vitest-environment jsdom */

import { afterEach, describe, expect, it } from "vitest";
import { installInputCapitalization } from "../../app/inputCapitalization";

let uninstall: (() => void) | null = null;

function focusNew<K extends keyof HTMLElementTagNameMap>(tag: K, attrs: Record<string, string> = {}): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
  if (tag === "div") el.tabIndex = 0;
  document.body.appendChild(el);
  el.focus();
  return el;
}

afterEach(() => {
  uninstall?.();
  uninstall = null;
  document.body.innerHTML = "";
});

describe("installInputCapitalization", () => {
  it("turns off capitalisation and autocorrect on a focused text input", () => {
    uninstall = installInputCapitalization();
    const input = focusNew("input", { type: "text" });
    expect(input.getAttribute("autocapitalize")).toBe("off");
    expect(input.getAttribute("autocorrect")).toBe("off");
  });

  it("covers inputs without a type, textareas and contenteditable elements", () => {
    uninstall = installInputCapitalization();
    expect(focusNew("input").getAttribute("autocapitalize")).toBe("off");
    expect(focusNew("textarea").getAttribute("autocapitalize")).toBe("off");
    expect(focusNew("div", { contenteditable: "true" }).getAttribute("autocapitalize")).toBe("off");
  });

  it("keeps explicit attributes so a field can opt out", () => {
    uninstall = installInputCapitalization();
    const input = focusNew("input", { autocapitalize: "sentences", autocorrect: "on" });
    expect(input.getAttribute("autocapitalize")).toBe("sentences");
    expect(input.getAttribute("autocorrect")).toBe("on");
  });

  it("leaves non-text controls and non-editable elements alone", () => {
    uninstall = installInputCapitalization();
    expect(focusNew("input", { type: "checkbox" }).hasAttribute("autocapitalize")).toBe(false);
    expect(focusNew("div", { contenteditable: "false" }).hasAttribute("autocapitalize")).toBe(false);
    expect(focusNew("div").hasAttribute("autocapitalize")).toBe(false);
  });

  it("reaches a field inside an open shadow root", () => {
    uninstall = installInputCapitalization();
    const host = document.createElement("div");
    document.body.appendChild(host);
    const inner = document.createElement("input");
    host.attachShadow({ mode: "open" }).appendChild(inner);
    inner.focus();
    expect(inner.getAttribute("autocapitalize")).toBe("off");
    expect(host.hasAttribute("autocapitalize")).toBe(false);
  });

  it("handles a field that is already focused at install time", () => {
    const input = focusNew("input");
    uninstall = installInputCapitalization();
    expect(input.getAttribute("autocapitalize")).toBe("off");
  });

  it("installing twice registers one listener and the remover stops it", () => {
    const first = installInputCapitalization();
    const second = installInputCapitalization();
    expect(second).toBe(first);
    const calls: string[] = [];
    const original = HTMLInputElement.prototype.setAttribute;
    HTMLInputElement.prototype.setAttribute = function (name: string, value: string) {
      calls.push(name);
      original.call(this, name, value);
    };
    try {
      focusNew("input");
    } finally {
      HTMLInputElement.prototype.setAttribute = original;
    }
    expect(calls.filter((name) => name === "autocapitalize")).toHaveLength(1);
    first();
    const later = focusNew("input");
    expect(later.hasAttribute("autocapitalize")).toBe(false);
  });

  it("sets the attributes on pointerdown, before focus, without cancelling the event", () => {
    uninstall = installInputCapitalization();
    const input = document.createElement("input");
    document.body.appendChild(input);
    const event = new Event("pointerdown", { bubbles: true, cancelable: true });
    input.dispatchEvent(event);
    expect(input.getAttribute("autocapitalize")).toBe("off");
    expect(event.defaultPrevented).toBe(false);
    uninstall();
    uninstall = null;
    const other = document.createElement("input");
    document.body.appendChild(other);
    other.dispatchEvent(new Event("pointerdown", { bubbles: true }));
    expect(other.hasAttribute("autocapitalize")).toBe(false);
  });
});
