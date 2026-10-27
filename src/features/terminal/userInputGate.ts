// Tells whether terminal onData is being raised by a user input event (keys,
// paste, IME, focus, mouse) rather than by xterm parsing output. A capture
// listener on the terminal host marks the event before xterm's own listeners
// run; the mark ends in the bubble phase of the same event, or, when xterm
// stops its propagation, on the next macrotask. A trusted event runs its
// listeners with microtask checkpoints in between, so a microtask would end
// the mark too early.

const MARKED = ["keydown", "keypress", "input", "paste", "compositionend", "focus", "blur", "mousedown", "mouseup", "wheel"];
// focus and blur do not bubble: their bubbling twins fire right after them.
const CLEARED = ["keydown", "keypress", "input", "paste", "compositionend", "focusin", "focusout", "mousedown", "mouseup", "wheel"];

export interface UserInputGate {
  active: () => boolean;
  dispose: () => void;
}

export function createUserInputGate(host: HTMLElement): UserInputGate {
  let active = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const clear = () => {
    active = false;
    if (timer !== undefined) {
      clearTimeout(timer);
      timer = undefined;
    }
  };
  const mark = () => {
    active = true;
    if (timer === undefined) timer = setTimeout(clear, 0);
  };
  for (const type of MARKED) host.addEventListener(type, mark, true);
  for (const type of CLEARED) host.addEventListener(type, clear, false);
  return {
    active: () => active,
    dispose: () => {
      for (const type of MARKED) host.removeEventListener(type, mark, true);
      for (const type of CLEARED) host.removeEventListener(type, clear, false);
      clear();
    },
  };
}
