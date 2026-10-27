import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";

// MovableHost renders its children once into a stable element and moves that element between its own place and an
// external target, so the children keep their state (and any work they run) when they change location. The element
// is display: contents, so the children lay out as if they were direct children of wherever it sits.
export function MovableHost({ target, children }: { target: HTMLElement | null; children: ReactNode }) {
  const [node] = useState(() => {
    const element = document.createElement("div");
    element.className = "nx-movable-host";
    element.style.display = "contents";
    return element;
  });
  const homeRef = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    const parent = target ?? homeRef.current;
    if (parent && node.parentNode !== parent) parent.appendChild(node);
  }, [target, node]);

  useLayoutEffect(() => () => node.remove(), [node]);

  return (
    <>
      <div ref={homeRef} style={{ display: "contents" }} />
      {createPortal(children, node)}
    </>
  );
}
