/** @vitest-environment jsdom */
// 设备会话账号边界测试: 身份变化关掉设备类工作区并断开会话, 旧账号 shell 不留给新账号;
// 非设备工作区不受影响; mount 时同身份不误清。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { flushUntil, mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
	(window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
	return {
		disconnect: vi.fn((_sessionId: string) => Promise.resolve()),
		closeTab: vi.fn((_id: string, _mode?: "kill" | "detach") => Promise.resolve(true)),
	};
});

vi.mock("../../ipc/commands", async (importOriginal) => {
	const original = await importOriginal<typeof import("../../ipc/commands")>();
	return { ...original, sessionApi: { ...original.sessionApi, disconnect: mocks.disconnect } };
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
				panes: [{ id: "p1", activeTabId: "t1", tabs: [{ id: "t1", kind: "terminal", sessionId: "s-dev", title: "终端", closable: true }] }],
				activePaneId: "p1",
				splitRatio: 0.5,
			},
			{
				id: "ws-ssh",
				kind: "session",
				title: "prod",
				closable: true,
				sessionId: "s-ssh",
				assetId: "a-1",
				assetKind: "ssh",
				panes: [{ id: "p2", activeTabId: "t2", tabs: [{ id: "t2", kind: "terminal", sessionId: "s-ssh", title: "终端", closable: true }] }],
				activePaneId: "p2",
				splitRatio: 0.5,
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

describe("设备会话账号边界", () => {
	it("账号 A->B: 设备工作区关闭且会话断开, ssh 工作区保留", async () => {
		useAuth.setState({ user: OTHER_USER });
		await flushUntil(() => useUi.getState().workspaces.every((w) => w.assetKind !== "device"));

		expect(useUi.getState().workspaces.map((w) => w.id)).toEqual(["ws-ssh"]);
		expect(mocks.closeTab).toHaveBeenCalledWith("t1");
		expect(mocks.disconnect).toHaveBeenCalledTimes(1);
		expect(mocks.disconnect).toHaveBeenCalledWith("s-dev");
		expect(useUi.getState().activeWorkspaceId).toBe("ws-ssh");
	});

	it("登出 (user 变 null): 设备工作区同样清理", async () => {
		useAuth.setState({ user: null });
		await flushUntil(() => useUi.getState().workspaces.every((w) => w.assetKind !== "device"));

		expect(mocks.disconnect).toHaveBeenCalledWith("s-dev");
		expect(useUi.getState().workspaces.map((w) => w.id)).toEqual(["ws-ssh"]);
	});

	it("同身份刷新不清设备工作区", async () => {
		useAuth.setState({ user: { ...USER } });
		await flushUntil(() => mocks.disconnect.mock.calls.length === 0 && useUi.getState().workspaces.length === 2);
		expect(useUi.getState().workspaces.some((w) => w.assetKind === "device")).toBe(true);
	});
});
