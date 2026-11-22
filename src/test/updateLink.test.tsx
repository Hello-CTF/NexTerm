/** @vitest-environment jsdom */

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const harness = vi.hoisted(() => ({ desktop: true, open: vi.fn() }));

vi.mock("../ipc/env", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../ipc/env")>()),
  get DESKTOP() {
    return harness.desktop;
  },
}));
vi.mock("../ipc/wails", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../ipc/wails")>()),
  openExternalURL: harness.open,
}));

import { UpdateLink } from "../app/UpdateLink";
import { useUi } from "../app/store";

const URL = "https://github.com/Hello-CTF/NexTerm/releases/tag/v9.9.9";
let root: Root | null = null;
let container: HTMLDivElement;

function render(): HTMLAnchorElement {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  act(() => root!.render(<UpdateLink href={URL}>releases</UpdateLink>));
  return container.querySelector("a")!;
}

function click(link: HTMLAnchorElement): MouseEvent {
  const event = new MouseEvent("click", { bubbles: true, cancelable: true });
  act(() => {
    link.dispatchEvent(event);
  });
  return event;
}

beforeEach(() => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  harness.desktop = true;
  harness.open.mockReset();
  useUi.setState({ toasts: [] });
});

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = null;
  container?.remove();
});

describe("UpdateLink", () => {
  it("opens the page in the system browser on desktop instead of navigating the app", async () => {
    harness.open.mockResolvedValue(undefined);
    const link = render();
    expect(link.getAttribute("href")).toBe(URL);
    const event = click(link);
    expect(event.defaultPrevented).toBe(true);
    expect(harness.open).toHaveBeenCalledWith(URL);
    await act(async () => {});
    expect(useUi.getState().toasts).toHaveLength(0);
  });

  it("tells the user when the system browser cannot be opened", async () => {
    harness.open.mockRejectedValue(new Error("no browser"));
    click(render());
    await act(async () => {});
    const toasts = useUi.getState().toasts;
    expect(toasts).toHaveLength(1);
    expect(toasts[0]).toMatchObject({ kind: "error" });
    expect(toasts[0].text).toContain("no browser");
  });

  it("leaves a regular new-tab link in the browser build", () => {
    harness.desktop = false;
    const link = render();
    expect(link.target).toBe("_blank");
    expect(link.rel).toBe("noreferrer");
    const event = click(link);
    expect(event.defaultPrevented).toBe(false);
    expect(harness.open).not.toHaveBeenCalled();
  });
});
