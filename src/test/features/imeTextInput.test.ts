// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { Terminal } from "@xterm/xterm";
import { attachImeTextInput } from "../../features/terminal/imeTextInput";

function setup() {
  const host = document.createElement("div");
  document.body.appendChild(host);
  const term = new Terminal();
  term.open(host);
  const detach = attachImeTextInput(term, host);
  const sent: string[] = [];
  term.onData((data) => sent.push(data));
  const textarea = term.textarea!;
  const key = (type: "keydown" | "keyup", key: string, keyCode: number, shiftKey = false) => {
    const event = new KeyboardEvent(type, { key, shiftKey, bubbles: true, cancelable: true });
    Object.defineProperty(event, "keyCode", { get: () => keyCode });
    textarea.dispatchEvent(event);
  };
  const press = (char: string, shiftKey = false) => {
    const event = new KeyboardEvent("keypress", { key: char, charCode: char.charCodeAt(0), shiftKey, bubbles: true, cancelable: true });
    textarea.dispatchEvent(event);
  };
  const insert = (data: string) => {
    textarea.value += data;
    textarea.setSelectionRange(textarea.value.length, textarea.value.length);
    textarea.dispatchEvent(new InputEvent("input", { inputType: "insertText", data, bubbles: true, composed: true }));
  };
  const cleanup = () => {
    detach();
    term.dispose();
    host.remove();
  };
  return { sent, textarea, key, press, insert, cleanup };
}

const settle = () => new Promise((resolve) => setTimeout(resolve, 20));

describe("IME committed text", () => {
  let cleanup: (() => void) | null = null;
  afterEach(() => {
    cleanup?.();
    cleanup = null;
  });

  it("sends a full-width question mark typed with Shift held when the insertion precedes its keydown", async () => {
    const view = setup();
    cleanup = view.cleanup;
    view.key("keydown", "Shift", 16, true);
    for (let i = 0; i < 3; i++) {
      view.insert("？");
      view.key("keydown", "？", 229, true);
      await settle();
      view.key("keyup", "/", 191, true);
    }
    view.key("keyup", "Shift", 16);
    await settle();
    expect(view.sent).toEqual(["？", "？", "？"]);
  });

  it("sends the character once when the 229 keydown precedes the insertion", async () => {
    const view = setup();
    cleanup = view.cleanup;
    view.textarea.value = "你好";
    view.key("keydown", "Shift", 16, true);
    view.key("keydown", "Process", 229, true);
    view.insert("？");
    await settle();
    view.key("keyup", "/", 191, true);
    view.key("keyup", "Shift", 16);
    await settle();
    expect(view.sent).toEqual(["？"]);
    expect(view.textarea.value).toBe("你好");
  });

  it("sends an uppercase letter once when the insertion follows the keypress xterm already sent", async () => {
    const view = setup();
    cleanup = view.cleanup;
    view.key("keydown", "Shift", 16, true);
    view.key("keydown", "H", 72, true);
    view.press("H", true);
    view.insert("H");
    await settle();
    view.key("keyup", "H", 72, true);
    view.key("keyup", "Shift", 16);
    await settle();
    expect(view.sent).toEqual(["H"]);
  });

  it("leaves composition input to xterm", async () => {
    const view = setup();
    cleanup = view.cleanup;
    view.textarea.value = "ni";
    view.textarea.dispatchEvent(
      new InputEvent("input", { inputType: "insertCompositionText", data: "ni", isComposing: true, bubbles: true }),
    );
    await settle();
    expect(view.sent).toEqual([]);
    expect(view.textarea.value).toBe("ni");
  });
});
