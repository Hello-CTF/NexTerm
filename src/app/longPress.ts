const LONG_PRESS_MS = 500;
const MOVE_TOLERANCE_PX = 12;

export function installLongPressContextMenu(): void {
  let timer = 0;
  let startX = 0;
  let startY = 0;
  let target: EventTarget | null = null;
  let fired = false;

  const cancel = () => {
    window.clearTimeout(timer);
    timer = 0;
    target = null;
  };

  window.addEventListener(
    "touchstart",
    (event) => {
      fired = false;
      if (event.touches.length !== 1) {
        cancel();
        return;
      }
      const el = event.target instanceof Element ? event.target : null;
      // Long-press inside xterm is its own text selection; do not intercept
      if (!el || el.closest(".xterm")) return;
      const touch = event.touches[0];
      startX = touch.clientX;
      startY = touch.clientY;
      target = event.target;
      timer = window.setTimeout(() => {
        timer = 0;
        if (!(target instanceof Element) || !target.isConnected) return;
        fired = true;
        target.dispatchEvent(
          new MouseEvent("contextmenu", {
            bubbles: true,
            cancelable: true,
            clientX: startX,
            clientY: startY,
            button: 2,
            buttons: 2,
          }),
        );
      }, LONG_PRESS_MS);
    },
    { passive: true },
  );

  window.addEventListener(
    "touchmove",
    (event) => {
      if (!timer) return;
      const touch = event.touches[0];
      if (
        Math.abs(touch.clientX - startX) > MOVE_TOLERANCE_PX ||
        Math.abs(touch.clientY - startY) > MOVE_TOLERANCE_PX
      ) {
        cancel();
      }
    },
    { passive: true },
  );
  window.addEventListener("touchend", cancel, { passive: true });
  window.addEventListener("touchcancel", cancel, { passive: true });

  // Swallow the click right after lifting the finger when the menu is already open, to avoid accidental select/activate
  window.addEventListener(
    "click",
    (event) => {
      if (!fired) return;
      fired = false;
      event.preventDefault();
      event.stopPropagation();
    },
    true,
  );
}
