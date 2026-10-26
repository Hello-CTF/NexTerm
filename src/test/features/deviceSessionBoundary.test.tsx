/** @vitest-environment jsdom */
// Device-session account boundary tests: an identity change closes device-type workspaces and disconnects sessions, no old-account shell is left for the new account;
// non-device workspaces are unaffected; same identity on mount is not wrongly cleared.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, flushUntil, mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
	(window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
	return {
		disconnect: vi.fn((_sessionId: string) => Promise.resolve()),
		closeTab: vi.fn((_id: string, _mode?: "kill" | "detach") => Promise.resolve(true)),
	};
});

vi.mock("../../ipc/commands", async (importOriginal) => {
	const { createCommandsMock } = await import("../helpers/mockCommands");
	return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
		sessionApi: { disconnect: mocks.disconnect },
	});
});

import { DeviceSessionBoundary } from "../../features/fleet/DeviceSessionBoundary";
import { useAuth } from "../../features/auth/store";
import { useUi } from "../../app/store";

const USER = {
	id: "u-1",
	username: "alice",
	display_name: "Alice",
	role: "user" as const,
	state: "active" as const,
	must_change_password: false,
	mfa_enabled: false,
	created_at: 1,
	updated_at: 1,
	last_login_at: 1,
};
const OTHER_USER = { ...USER, id: "u-2", username: "bob" };

let mounted: MountedView | null = null;

function seed(): void {
	useUi.setState({
		closeTab: mocks.closeTab,
		sessions: [
			{ id: "s-dev", assetId: "device/d-1", assetName: "web-01", kind: "device", status: "connected", controller: "" },
			{ id: "s-ssh", assetId: "a-1", assetName: "prod", kind: "ssh", status: "connected", controller: "" },
		] as never,
		workspaces: [
			{
				id: "ws-dev",
				kind: "session",
				title: "web-01",
				closable: true,
				sessionId: "s-dev",
				assetId: "device/d-1",
				assetKind: "device",
				panes: [{ id: "p1", activeTabId: "t1", tabs: [{ id: "t1", kind: "terminal", sessionId: "s-dev", title: "Terminal", closable: true }] }],
				activePaneId: "p1",
			},
			{
				id: "ws-ssh",
				kind: "session",
				title: "prod",
				closable: true,
				sessionId: "s-ssh",
				assetId: "a-1",
				assetKind: "ssh",
				panes: [{ id: "p2", activeTabId: "t2", tabs: [{ id: "t2", kind: "terminal", sessionId: "s-ssh", title: "Terminal", closable: true }] }],
				activePaneId: "p2",
			},
		] as never,
		activeWorkspaceId: "ws-dev",
	});
}

beforeEach(() => {
	mocks.disconnect.mockClear();
	mocks.closeTab.mockClear();
	useAuth.setState({ user: USER });
	seed();
	mounted = mountWithClient(createElement(DeviceSessionBoundary));
});

afterEach(() => {
	mounted?.unmount();
	mounted = null;
	useUi.setState({ workspaces: [], sessions: [], activeWorkspaceId: null });
	useAuth.setState({ user: null });
});

describe("Device session account boundaries", () => {
	it("account A to B: closes device workspaces and disconnects their sessions, preserves SSH workspaces", async () => {
		act(() => {
			useAuth.setState({ user: OTHER_USER });
		});
		await flushUntil(() => useUi.getState().workspaces.every((w) => w.assetKind !== "device"));

		expect(useUi.getState().workspaces.map((w) => w.id)).toEqual(["ws-ssh"]);
		expect(mocks.closeTab).toHaveBeenCalledWith("t1", "kill");
		expect(mocks.disconnect).toHaveBeenCalledTimes(1);
		expect(mocks.disconnect).toHaveBeenCalledWith("s-dev");
		expect(useUi.getState().activeWorkspaceId).toBe("ws-ssh");
	});

	it("logout (user becomes null): also cleans up device workspaces", async () => {
		act(() => {
			useAuth.setState({ user: null });
		});
		await flushUntil(() => useUi.getState().workspaces.every((w) => w.assetKind !== "device"));

		expect(mocks.disconnect).toHaveBeenCalledWith("s-dev");
		expect(useUi.getState().workspaces.map((w) => w.id)).toEqual(["ws-ssh"]);
	});

	it("identity resolved from null (desktop startup order): not treated as an account switch, preserves device workspaces", async () => {
		mounted?.unmount();
		useAuth.setState({ user: null });
		mounted = mountWithClient(createElement(DeviceSessionBoundary));
		await flush();
		act(() => {
			useAuth.setState({ user: USER });
		});
		await flush();
		expect(useUi.getState().workspaces.some((w) => w.assetKind === "device")).toBe(true);
		expect(mocks.disconnect).not.toHaveBeenCalled();
	});

	it("refreshing with the same identity does not clear device workspaces", async () => {
		act(() => {
			useAuth.setState({ user: { ...USER } });
		});
		await flushUntil(() => mocks.disconnect.mock.calls.length === 0 && useUi.getState().workspaces.length === 2);
		expect(useUi.getState().workspaces.some((w) => w.assetKind === "device")).toBe(true);
	});
});
