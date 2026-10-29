/** @vitest-environment jsdom */
// PreviewShareMenu inside the terminal toolbar: the overflow menu opens the panel through openRequest, the standalone
// button is hidden on narrow panes, and the active-preview state is reported so the host can keep sharing visible.
import { act, createElement, type ComponentProps } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flush } from "./reactTestUtils";
import { PreviewShareMenu } from "../../features/sharing/PreviewShareMenu";
import { setCsrfToken } from "../../ipc/authApi";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return { fetch: vi.fn() };
});

const TAB_ID = "tab-7";

function respond(payload: unknown) {
  return { ok: true, status: 200, text: async () => JSON.stringify(payload) };
}

let root: Root | null = null;
let container: HTMLDivElement | null = null;

async function render(props: Partial<ComponentProps<typeof PreviewShareMenu>>) {
  if (!container) {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  }
  await act(async () => {
    root!.render(createElement(PreviewShareMenu, { tabId: TAB_ID, sessionName: "prod", ...props }));
  });
  await flush();
}

beforeEach(() => {
  vi.clearAllMocks();
  setCsrfToken("csrf-1");
  vi.stubGlobal("fetch", mocks.fetch);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  container?.remove();
  root = null;
  container = null;
  vi.unstubAllGlobals();
});

describe("PreviewShareMenu toolbar integration", () => {
  it("opens the panel when openRequest increases and ignores the initial value", async () => {
    mocks.fetch.mockImplementation(async () => respond({ links: [] }));
    await render({ openRequest: 3 });
    expect(container!.textContent).not.toContain("创建链接");
    await render({ openRequest: 4 });
    expect(container!.textContent).toContain("创建链接");
  });

  it("keeps the standalone button on desktop only and anchors the panel to the viewport on narrow panes", async () => {
    mocks.fetch.mockImplementation(async () => respond({ links: [] }));
    await render({ openRequest: 0 });
    const button = container!.querySelector("button[title='创建预览链接']")!;
    expect(button.className).toContain("@max-[560px]:hidden");
    await render({ openRequest: 1 });
    const panel = container!.querySelector("div.absolute")!;
    expect(panel.className).toContain("@max-[560px]:fixed");
  });

  it("reports whether an active preview exists", async () => {
    const onActiveChange = vi.fn();
    mocks.fetch.mockImplementation(async () =>
      respond({
        links: [{ id: "lnk-1", session_id: TAB_ID, created_at: 1, expires_at: Date.now() + 3_600_000 }],
      }),
    );
    await render({ onActiveChange });
    expect(onActiveChange).toHaveBeenLastCalledWith(true);
  });

  it("reports no active preview when the list is empty and does not load the list without a listener", async () => {
    const onActiveChange = vi.fn();
    mocks.fetch.mockImplementation(async () => respond({ links: [] }));
    await render({ onActiveChange });
    expect(onActiveChange).toHaveBeenLastCalledWith(false);
    mocks.fetch.mockClear();
    await act(async () => root?.unmount());
    container?.remove();
    root = null;
    container = null;
    await render({});
    expect(mocks.fetch).not.toHaveBeenCalled();
  });
});
