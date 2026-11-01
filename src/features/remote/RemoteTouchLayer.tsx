import { useEffect, useRef } from "react";
import { GestureRecognizer, type GestureAction, type TouchMode } from "./touchGestures";

function dispatchMouse(target: HTMLElement, type: string, x: number, y: number, button: number, buttons: number): void {
  target.dispatchEvent(
    new MouseEvent(type, { bubbles: true, cancelable: true, composed: true, clientX: x, clientY: y, button, buttons, view: window }),
  );
}

// RemoteTouchLayer sits over the remote screen on touch devices and replaces the browser's touch handling there:
// the page must not scroll, zoom or open a context menu from a finger on the remote desktop. It forwards the
// recognized gestures to the remote canvas as the mouse events that canvas already understands.
export function RemoteTouchLayer({
  mode,
  getTarget,
  onPinch,
  onPan,
  onFocusRequest,
}: {
  mode: TouchMode;
  getTarget: () => HTMLElement | null;
  onPinch: (factor: number) => void;
  onPan: (dx: number, dy: number) => void;
  onFocusRequest: () => void;
}) {
  const layerRef = useRef<HTMLDivElement>(null);
  const recognizerRef = useRef<GestureRecognizer | null>(null);
  const handlers = useRef({ getTarget, onPinch, onPan, onFocusRequest });
  handlers.current = { getTarget, onPinch, onPan, onFocusRequest };

  useEffect(() => {
    const layer = layerRef.current;
    if (!layer) return;
    const points = (event: TouchEvent) => [...event.touches].map((touch) => ({ x: touch.clientX, y: touch.clientY }));
    let pressed = 0;
    const apply = (action: GestureAction) => {
      const target = handlers.current.getTarget();
      switch (action.type) {
        case "pinch":
          handlers.current.onPinch(action.factor);
          return;
        case "pan":
          handlers.current.onPan(action.dx, action.dy);
          return;
        case "move":
          if (target) dispatchMouse(target, "mousemove", action.at.x, action.at.y, 0, pressed);
          return;
        case "button": {
          if (!target) return;
          const mask = action.button === 2 ? 2 : 1;
          pressed = action.down ? pressed | mask : pressed & ~mask;
          dispatchMouse(target, action.down ? "mousedown" : "mouseup", action.at.x, action.at.y, action.button, pressed);
          return;
        }
        case "wheel":
          if (target) {
            target.dispatchEvent(
              new WheelEvent("wheel", { bubbles: true, cancelable: true, composed: true, clientX: action.at.x, clientY: action.at.y, deltaX: action.deltaX, deltaY: action.deltaY, deltaMode: 0 }),
            );
          }
      }
    };
    const rect = layer.getBoundingClientRect();
    const recognizer = new GestureRecognizer("direct", apply, { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 });
    recognizerRef.current = recognizer;
    const onStart = (event: TouchEvent) => {
      event.preventDefault();
      handlers.current.onFocusRequest();
      recognizer.touchStart(points(event));
    };
    const onMove = (event: TouchEvent) => {
      event.preventDefault();
      recognizer.touchMove(points(event));
    };
    const onEnd = (event: TouchEvent) => {
      event.preventDefault();
      recognizer.touchEnd(event.touches.length);
    };
    const onCancel = () => recognizer.cancel();
    // Not passive: the layer must be able to stop the page from scrolling under the finger.
    layer.addEventListener("touchstart", onStart, { passive: false });
    layer.addEventListener("touchmove", onMove, { passive: false });
    layer.addEventListener("touchend", onEnd, { passive: false });
    layer.addEventListener("touchcancel", onCancel);
    return () => {
      recognizer.cancel();
      layer.removeEventListener("touchstart", onStart);
      layer.removeEventListener("touchmove", onMove);
      layer.removeEventListener("touchend", onEnd);
      layer.removeEventListener("touchcancel", onCancel);
    };
  }, []);

  useEffect(() => {
    recognizerRef.current?.setMode(mode);
  }, [mode]);

  return (
    <div
      ref={layerRef}
      data-remote-surface=""
      data-testid="remote-touch-layer"
      className="absolute inset-0 z-10"
      style={{ touchAction: "none", WebkitTouchCallout: "none", userSelect: "none" }}
      onContextMenu={(event) => event.preventDefault()}
    />
  );
}
