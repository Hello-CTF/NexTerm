// Tells whether terminal onData is being raised by a user input event (keys,
// paste, IME, focus, mouse) rather than by xterm parsing output. A capture
// listener on the terminal host marks the event before xterm's own listeners
// run. The mark ends in a one-shot listener added to the event's target: it
// runs after xterm's handler on the same node, even when that handler stops
// propagation (xterm's keydown does). When it cannot run (the target is the
// host itself, or a handler stopped immediate propagation) the mark ends on
// the next macrotask. A trusted event runs its listeners with microtask
// checkpoints in between, so a microtask would end the mark too early.

const MARKED = ["keydown", "keypress", "input", "paste", "compositionend", "focus", "blur", "mousedown", "mouseup", "wheel"];

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
  const mark = (event: Event) => {
    active = true;
    if (timer === undefined) timer = setTimeout(clear, 0);
    if (event.target !== host) event.target?.addEventListener(event.type, clear, { once: true });
  };
  for (const type of MARKED) host.addEventListener(type, mark, true);
  return {
    active: () => active,
    dispose: () => {
      for (const type of MARKED) host.removeEventListener(type, mark, true);
      clear();
    },
  };
}
