/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { mount, type MountedView } from "./reactTestUtils";

const harness = vi.hoisted(() => ({ production: new Set<string>() }));

vi.mock("../../features/production/productionSettings", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../features/production/productionSettings")>();
  return {
    ...actual,
    useAssetProductionClassification: (assetId: string | null | undefined) =>
      assetId && harness.production.has(assetId)
        ? { production: true, environment: "production", matchedBy: "tag" }
        : null,
  };
});

import { AppHeader } from "../../app/shell/AppHeader";
import { TabProductionBadge } from "../../app/workspace/tabStrip";
import { useUi, type AppTab, type Workspace } from "../../app/store";
import type { SessionInfo } from "../../ipc/commands";

let view: MountedView | null = null;

const SESSIONS = [
  { id: "s-prod", assetId: "a-prod", status: "connected" },
  { id: "s-dev", assetId: "a-dev", status: "connected" },
] as unknown as SessionInfo[];

function tab(over: Partial<AppTab>): AppTab {
  return { id: "t1", kind: "terminal", title: "shell", sessionId: "s-prod", closable: true, ...over } as AppTab;
}

function header(activeTab: AppTab | null) {
  const ws = { id: "w1", title: "ws", sessionId: "s-prod", kind: "ssh", panes: [] } as unknown as Workspace;
  view = mount(
    createElement(AppHeader, {
      ws,
      activeTab,
      rightDockOpen: false,
      commandPaletteLabel: "Ctrl+Shift+P",
      onOpenWorkspaceSwitcher: () => {},
      onToggleRight: () => {},
      onOpenPalette: () => {},
    }),
  );
}

beforeEach(() => {
  harness.production = new Set(["a-prod"]);
  useUi.setState({ sessions: SESSIONS } as never);
});

afterEach(() => {
  view?.unmount();
  view = null;
});

describe("production host marking", () => {
  it("shows a header marker for a production terminal", () => {
    header(tab({}));
    const marker = document.querySelector('[aria-label="生产环境：production"]');
    expect(marker).not.toBeNull();
    expect(marker?.getAttribute("title")).toBe("生产环境：production");
  });

  it("shows no marker for a non-production terminal", () => {
    header(tab({ sessionId: "s-dev" }));
    expect(document.querySelector('[aria-label^="生产环境"]')).toBeNull();
  });

  it("shows no marker for a tab that is not a terminal", () => {
    header(tab({ kind: "devices" } as Partial<AppTab>));
    expect(document.querySelector('[aria-label^="生产环境"]')).toBeNull();
  });

  it("gives the tab badge a class the tab strip can style", () => {
    view = mount(createElement(TabProductionBadge, { sessionId: "s-prod" }));
    expect(document.querySelector(".nx-tab-production-badge")?.textContent).toBe("生产");
  });
});
