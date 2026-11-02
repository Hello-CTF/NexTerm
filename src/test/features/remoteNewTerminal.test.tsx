/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";

const mocks = vi.hoisted(() => ({ connect: vi.fn(), reconnectDeadHost: vi.fn() }));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), { sessionApi: { connect: mocks.connect } });
});

import { useWorkspaceActions } from "../../app/shell/useWorkspaceActions";
import { useUi, type Workspace } from "../../app/store";
import * as store from "../../app/store";
import { flush, mount, type MountedView } from "./reactTestUtils";

const remote: Workspace = {
  id: "ws-r",
  kind: "remote",
  title: "win-01",
  assetId: "asset-1",
  assetKind: "rdp",
  panes: [{ id: "p", tabs: [], activeTabId: null }],
  activePaneId: "p",
  closable: true,
};

let view: MountedView | undefined;
afterEach(() => {
  view?.unmount();
  view = undefined;
  vi.restoreAllMocks();
});

describe("new terminal in a remote desktop workspace", () => {
  it("does not try to connect the remote asset as a host session", async () => {
    const reconnect = vi.spyOn(store, "reconnectDeadHost").mockRejectedValue(new Error("must not be called"));
    useUi.setState({ workspaces: [remote], activeWorkspaceId: "ws-r", sessions: [] });
    let openNewTerminal!: () => Promise<void>;
    function Harness() {
      openNewTerminal = useWorkspaceActions({ ws: remote, backgroundAvailable: false, openDatabasePicker: () => undefined }).openNewTerminal;
      return null;
    }
    view = mount(createElement(Harness));
    await act(async () => {
      await openNewTerminal();
    });
    await flush();
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(reconnect).not.toHaveBeenCalled();
  });
});
