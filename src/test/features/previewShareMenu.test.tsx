/** @vitest-environment jsdom */
// PreviewShareMenu creation entry: create a preview link, show and copy the public URL on the spot, list in-progress
// previews and revoke them. Read-only by default (stated in the description copy); the token appears only once, in the creation response.
import { act, createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mount, flush, clickButton, type MountedView } from "./reactTestUtils";
import { PreviewShareMenu } from "../../features/sharing/PreviewShareMenu";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return { fetch: vi.fn() };
});

const TAB_ID = "tab-42";

function respond(payload: unknown, status = 200) {
  return { ok: status >= 200 && status < 300, status, text: async () => JSON.stringify(payload) };
}

function queueResponses(...payloads: unknown[]): Array<Record<string, unknown>> {
  const bodies: Array<Record<string, unknown>> = [];
  let index = 0;
  mocks.fetch.mockImplementation(async (_url: string, init: RequestInit) => {
    if (init.body) bodies.push(JSON.parse(String(init.body)) as Record<string, unknown>);
    const payload = payloads[Math.min(index, payloads.length - 1)];
    index += 1;
    return respond(payload);
  });
  return bodies;
}

import { setCsrfToken } from "../../ipc/authApi";

async function mountMenu(): Promise<MountedView> {
  const view = mount(createElement(PreviewShareMenu, { tabId: TAB_ID, sessionName: "prod" }));
  await flush();
  clickButton(view.container, "预览链接");
  await flush();
  return view;
}

beforeEach(() => {
  vi.clearAllMocks();
  setCsrfToken("csrf-1");
  vi.stubGlobal("fetch", mocks.fetch);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("PreviewShareMenu", () => {
  it("creates a preview link and shows the public URL once", async () => {
    const bodies = queueResponses(
      { links: [] },
      { id: "lnk-1", session_id: TAB_ID, token: "one-shot-token", created_at: 1, expires_at: Date.now() + 3_600_000 },
      { links: [] },
    );
    const view = await mountMenu();
    expect(view.container.textContent).toContain("默认只读");
    expect(view.container.textContent).toContain("不能输入");
    clickButton(view.container, "创建链接");
    await flush();
    expect(bodies[0]).toMatchObject({ session_id: TAB_ID });
    expect(view.container.textContent).toContain(`${window.location.origin}/share/preview/one-shot-token`);
  });

  it("lists active previews for the tab and revokes them", async () => {
    queueResponses(
      {
        links: [
          { id: "lnk-1", session_id: TAB_ID, created_at: 1, expires_at: Date.now() + 3_600_000 },
          { id: "lnk-2", session_id: "other-tab", created_at: 1, expires_at: Date.now() + 3_600_000 },
          { id: "lnk-3", session_id: TAB_ID, created_at: 1, expires_at: Date.now() - 1000 },
        ],
      },
      { ok: true },
      { links: [] },
    );
    const view = await mountMenu();
    await flush();
    expect(view.container.textContent).toContain("进行中的预览");
    expect(view.container.textContent).not.toContain("one-shot-token");
    const revokeCallsBefore = mocks.fetch.mock.calls.filter((call) => String(call[0]).includes("/revoke")).length;
    clickButton(view.container, "吊销");
    await flush();
    const revokeCalls = mocks.fetch.mock.calls.filter((call) => String(call[0]).includes("/revoke"));
    expect(revokeCalls.length).toBe(revokeCallsBefore + 1);
    expect(String(revokeCalls[0]?.[0])).toContain("/share/previews/lnk-1/revoke");
  });

  it("closes on outside pointer down but not on inside pointer down", async () => {
    queueResponses({ links: [] });
    const view = await mountMenu();
    const select = view.container.querySelector("select");
    expect(select).not.toBeNull();
    act(() => {
      select?.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    });
    expect(view.container.textContent).toContain("创建链接");
    act(() => {
      document.body.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    });
    expect(view.container.textContent).not.toContain("创建链接");
    view.unmount();
  });

  it("surfaces create errors inline", async () => {
    mocks.fetch
      .mockImplementationOnce(async () => respond({ links: [] }))
      .mockImplementationOnce(async () => respond({ ok: false, error: { code: "forbidden", message: "terminal does not belong to this user" } }, 403));
    const view = await mountMenu();
    clickButton(view.container, "创建链接");
    await flush();
    expect(view.container.textContent).toContain("terminal does not belong to this user");
    expect(view.container.textContent).not.toContain("/share/preview/");
  });
});
