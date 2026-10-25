/** @vitest-environment jsdom */

import { Profiler, act, createElement } from "react";
import { describe, expect, it, vi } from "vitest";
import { VaultInitModal } from "../features/credentials/VaultInitModal";
import { useUi } from "../app/store";
import { mountWithClient } from "./features/reactTestUtils";

vi.mock("../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("./helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../ipc/commands")>(), {
    vaultApi: { initMaster: vi.fn() },
  });
});

describe("store selector subscriptions", () => {
  it("does not re-render a component when an unrelated store field changes", () => {
    const onRender = vi.fn();
    const view = mountWithClient(
      createElement(
        Profiler,
        { id: "modal", onRender },
        createElement(VaultInitModal, { onClose: () => {}, onInitialized: () => {} }),
      ),
    );
    const baseline = onRender.mock.calls.length;
    expect(baseline).toBeGreaterThan(0);

    act(() => {
      useUi.setState({ leftWidth: useUi.getState().leftWidth + 17 });
    });
    act(() => {
      useUi.getState().pushToast("info", "unrelated");
    });

    expect(onRender.mock.calls.length).toBe(baseline);
    view.unmount();
  });
});
