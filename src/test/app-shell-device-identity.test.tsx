/** @vitest-environment jsdom */
import { act, createElement } from "react";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flush, flushUntil, mount, type MountedView } from "./features/reactTestUtils";
import type { AccountUser } from "../ipc/authApi";

const mocks = vi.hoisted(() => {
	(window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
	return {
		assetList: vi.fn(),
		sessionList: vi.fn(),
		vaultStatus: vi.fn(),
		refresh: vi.fn(),
		disconnect: vi.fn(),
		closeTab: vi.fn(),
		logout: vi.fn(),
	};
});

vi.mock("../ipc/commands", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../ipc/commands")>();
	return {
		...actual,
		assetApi: { ...actual.assetApi, list: mocks.assetList },
		sessionApi: { ...actual.sessionApi, list: mocks.sessionList, disconnect: mocks.disconnect },
		vaultApi: { ...actual.vaultApi, status: mocks.vaultStatus },
	};
});

vi.mock("../ipc/authApi", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../ipc/authApi")>();
	return { ...actual, authApi: { ...actual.authApi, logout: mocks.logout } };
});

vi.mock("../app/layout", () => ({
	layoutBootstrapped: Promise.resolve(),
	startLayoutSync: () => {},
	advanceLayoutAccount: () => {},
	finishLayoutRestore: vi.fn(),
	layoutRestoreStatus: () => ({ pending: false, preview: false, count: 0 }),
	onRemoteChange: () => {},
	reviewPendingLayoutRestore: vi.fn(),
	subscribeLayoutRestore: () => () => {},
	undoLayoutRestore: vi.fn(),
}));
vi.mock("../app/unread", () => ({ startUnreadTracking: () => {}, resetUnreadTracking: () => {} }));
vi.mock("../features/settings/useWebAutoSync", () => ({
	useDesktopAutoSync: () => {},
	useWebAutoSync: () => {},
}));
vi.mock("../features/auth/AuthGate", () => ({ AuthGate: () => null }));
vi.mock("../features/terminal/TerminalPane", () => ({ TerminalPane: () => null }));
vi.mock("../features/terminal/BackgroundSessions", () => ({ BackgroundSessions: () => null }));
vi.mock("../features/terminal/TranscriptHistoryPanel", () => ({ TranscriptHistoryPanel: () => null }));
vi.mock("../features/files/FileBrowser", () => ({ FileBrowser: () => null }));
vi.mock("../features/files/FileTree", () => ({ FileTree: () => null }));
vi.mock("../features/files/MountPanel", () => ({ MountPanel: () => null }));
vi.mock("../features/files/FileEditor", () => ({ FileEditor: () => null }));
vi.mock("../features/files/LogViewer", () => ({ LogViewer: () => null }));
vi.mock("../features/forward/ForwardPanel", () => ({ ForwardPanel: () => null }));
vi.mock("../features/docker/DockerPanel", () => ({ DockerPanel: () => null }));
vi.mock("../features/db/DbPanel", () => ({ DbPanel: () => null }));
vi.mock("../features/ai/AiSidebar", () => ({ AiSidebar: () => null }));
vi.mock("../features/settings/SettingsView", () => ({ SettingsView: () => null }));
vi.mock("../features/settings/AuditView", () => ({ AuditView: () => null }));
vi.mock("../features/credentials/CredentialsPanel", () => ({ CredentialsPanel: () => null }));
vi.mock("../features/credentials/CredentialsSidebar", () => ({ CredentialsSidebar: () => null }));
vi.mock("../features/explorer/AssetTree", () => ({ AssetTree: () => null, AssetEditor: () => null }));
vi.mock("../app/JumpPalette", () => ({ JumpPalette: () => null }));
vi.mock("../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));
vi.mock("../app/UpdateBanner", () => ({ UpdateBanner: () => null }));
vi.mock("../app/DekUnlockBanner", () => ({ DekUnlockBanner: () => null }));

import App from "../app/App";
import { useUi } from "../app/store";
import { useAuth } from "../features/auth/store";

const USER: AccountUser = {
	id: "u-1",
	username: "alice",
	display_name: "Alice",
	role: "user",
	state: "active",
	must_change_password: false,
	mfa_enabled: false,
	created_at: 1,
	updated_at: 1,
	last_login_at: 1,
};
const OTHER_USER: AccountUser = { ...USER, id: "u-2", username: "bob" };
const originalResetAccountWorkspaces = useUi.getState().resetAccountWorkspaces;
const originalRefresh = useAuth.getState().refresh;

let mounted: MountedView | null = null;
let client: QueryClient | null = null;
let resetAccountWorkspaces: import("vitest").Mock<() => void>;
let order: string[] = [];

function seed(): void {
	resetAccountWorkspaces = vi.fn(() => {
		order.push("reset");
		originalResetAccountWorkspaces();
	});
	useUi.setState({
		closeTab: mocks.closeTab,
		resetAccountWorkspaces,
		toasts: [],
		leftOpen: true,
		rightOpen: false,
		leftMode: "assets",
		sessions: [
			{ id: "s-dev", assetId: "device/d-1", assetName: "web-01", kind: "device", status: "connected" },
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
		] as never,
		activeWorkspaceId: "ws-dev",
	});
}

async function mountReadyShell(): Promise<void> {
	client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	mounted = mount(createElement(QueryClientProvider, { client }, createElement(App)));
	await flushUntil(() => mocks.refresh.mock.calls.length > 0);
	await flush();
}

async function expectCleanupBeforeReset(user: AccountUser | null, gate: "ready" | "login"): Promise<void> {
	await mountReadyShell();
	expectDevicePreserved();
	act(() => {
		useAuth.setState({ user, gate });
	});
	await flushUntil(() => resetAccountWorkspaces.mock.calls.length === 1);
	await flush();

	expect(order).toEqual(["close:t1:kill", "disconnect:s-dev", "reset"]);
	expect(mocks.closeTab).toHaveBeenCalledTimes(1);
	expect(mocks.disconnect).toHaveBeenCalledTimes(1);
	expect(resetAccountWorkspaces).toHaveBeenCalledTimes(1);
	expect(useUi.getState().workspaces).toEqual([]);
}

function expectDevicePreserved(): void {
	expect(useUi.getState().workspaces.some((w) => w.assetKind === "device")).toBe(true);
	expect(mocks.closeTab).not.toHaveBeenCalled();
	expect(mocks.disconnect).not.toHaveBeenCalled();
	expect(resetAccountWorkspaces).not.toHaveBeenCalled();
}

// The shell loads these panels through React.lazy or dynamic imports. A cold import transforms the module
// in real time, which no tick budget bounds under machine load, so warm them before flushUntil counts ticks.
beforeAll(async () => {
	await Promise.all([
		import("../app/AutoSync"),
		import("../features/auth/AuthGate"),
	]);
});

beforeEach(() => {
	vi.clearAllMocks();
	order = [];
	document.body.replaceChildren();
	mocks.assetList.mockResolvedValue([]);
	mocks.sessionList.mockResolvedValue([]);
	mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
	mocks.refresh.mockResolvedValue(undefined);
	mocks.closeTab.mockImplementation((id: string, mode?: "kill" | "detach") => {
		order.push(`close:${id}:${mode}`);
		return Promise.resolve(true);
	});
	mocks.disconnect.mockImplementation((sessionId: string) => {
		order.push(`disconnect:${sessionId}`);
		return Promise.resolve();
	});
	mocks.logout.mockImplementation(() => {
		order.push("logout");
		return Promise.resolve({ ok: true });
	});
	useAuth.setState({
		status: { initialized: true, registration_open: false, auth: "on" },
		user: USER,
		gate: "ready",
		refresh: mocks.refresh,
		error: null, cleanupError: null,
	});
	seed();
});

afterEach(() => {
	mounted?.unmount();
	mounted = null;
	client?.clear();
	client = null;
	useUi.setState({
		resetAccountWorkspaces: originalResetAccountWorkspaces,
		workspaces: [],
		sessions: [],
		activeWorkspaceId: null,
	});
	useAuth.setState({ user: null, gate: "loading", refresh: originalRefresh, error: null, cleanupError: null });
});

describe("AppShell auth account cleanup ordering", () => {
	it("kills and disconnects a device exactly once before reset on account A to B", async () => {
		await expectCleanupBeforeReset(OTHER_USER, "ready");
	});

	it("kills and disconnects a device exactly once before reset on logout", async () => {
		await expectCleanupBeforeReset(null, "login");
	});

	it("cleans exactly once before revoking an active logout", async () => {
		await mountReadyShell();
		expectDevicePreserved();
		await act(async () => {
			await useAuth.getState().logout();
		});
		await flush();

		expect(order).toEqual(["close:t1:kill", "disconnect:s-dev", "logout", "reset"]);
		expect(mocks.closeTab).toHaveBeenCalledTimes(1);
		expect(mocks.disconnect).toHaveBeenCalledTimes(1);
		expect(mocks.logout).toHaveBeenCalledTimes(1);
		expect(useAuth.getState().gate).toBe("login");
		expect(useAuth.getState().error).toBeNull();
		expect(useUi.getState().workspaces).toEqual([]);
	});

	it("finishes active logout but retains visible cleanup failures and fallback evidence", async () => {
		await mountReadyShell();
		mocks.closeTab.mockImplementation((id: string, mode?: "kill" | "detach") => {
			order.push(`close:${id}:${mode}`);
			return Promise.resolve(false);
		});
		mocks.disconnect.mockImplementation((sessionId: string) => {
			order.push(`disconnect:${sessionId}`);
			return Promise.reject(new Error("offline"));
		});
		await act(async () => {
			await useAuth.getState().logout();
		});
		await flush();

		expect(order).toEqual(["close:t1:kill", "disconnect:s-dev", "logout", "reset"]);
		expect(useAuth.getState().gate).toBe("login");
		expect(useAuth.getState().error?.code).toBe("cleanup_failed");
		expect(useAuth.getState().error?.message).toContain("t1");
		expect(useAuth.getState().error?.message).toContain("s-dev");
		expect(useAuth.getState().error?.message).toContain("补充清理");
		expect(useUi.getState().toasts.some((toast) => toast.text.includes("设备连接未能全部关闭"))).toBe(true);
		expect(useUi.getState().workspaces).toEqual([]);
	});

	it("does not clean on initial mount or a same-account auth state refresh", async () => {
		await mountReadyShell();
		expectDevicePreserved();
		act(() => {
			useAuth.setState({ user: { ...USER } });
		});
		await flush();
		expectDevicePreserved();
	});

	it("does not kill or disconnect during initial null-to-user auth resolution", async () => {
		useAuth.setState({ user: null, gate: "loading" });
		await mountReadyShell();
		expectDevicePreserved();
		act(() => {
			useAuth.setState({ user: USER, gate: "ready" });
		});
		await flushUntil(() => resetAccountWorkspaces.mock.calls.length === 1);
		await flush();

		expect(order).toEqual(["reset"]);
		expect(mocks.closeTab).not.toHaveBeenCalled();
		expect(mocks.disconnect).not.toHaveBeenCalled();
	});
});
