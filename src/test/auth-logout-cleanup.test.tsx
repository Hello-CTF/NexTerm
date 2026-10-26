/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AccountUser } from "../ipc/authApi";

const mocks = vi.hoisted(() => {
	(window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
	return {
		closeTab: vi.fn(),
		terminalCloseTab: vi.fn(),
		disconnect: vi.fn(),
		logout: vi.fn(),
		logoutAll: vi.fn(),
		login: vi.fn(),
		dekGet: vi.fn(),
	};
});

vi.mock("../ipc/commands", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../ipc/commands")>();
	return {
		...actual,
		sessionApi: { ...actual.sessionApi, disconnect: mocks.disconnect },
		terminalApi: { ...actual.terminalApi, closeTab: mocks.terminalCloseTab },
	};
});

vi.mock("../ipc/authApi", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../ipc/authApi")>();
	return {
		...actual,
		authApi: {
			...actual.authApi,
			logout: mocks.logout,
			logoutAll: mocks.logoutAll,
			login: mocks.login,
			dekGet: mocks.dekGet,
		},
	};
});

vi.mock("../features/auth/crypto", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../features/auth/crypto")>();
	return { ...actual, unwrapDEKWithPassword: vi.fn().mockResolvedValue(new Uint8Array(32).fill(7)) };
});

import { useAuth } from "../features/auth/store";
import { useUi } from "../app/store";
import { getCsrfToken, setCsrfToken } from "../ipc/authApi";
import { DEVICE_SESSION_CLEANUP_TIMEOUT_MS } from "../features/fleet/deviceSessionCleanup";

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

let order: string[] = [];

function seed(): void {
	order = [];
	useUi.setState({
		closeTab: mocks.closeTab,
		toasts: [],
		sessions: [
			{ id: "s-dev", assetId: "device/d-1", assetName: "device", kind: "device", status: "connected", controller: "" },
		] as never,
		workspaces: [
			{
				id: "ws-dev",
				kind: "session",
				title: "device",
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
	useAuth.setState({
		status: { initialized: true, registration_open: false, auth: "on" },
		user: USER,
		dek: null,
		gate: "ready",
		error: null, cleanupError: null,
	});
	setCsrfToken("csrf-a");
}

function adoptB(): import("../app/store").Workspace {
	setCsrfToken("csrf-b");
	useAuth.setState({ user: { ...USER, display_name: "Alice next" }, gate: "ready" });
	const workspaceB = {
		id: "ws-b",
		kind: "session",
		title: "B device",
		closable: true,
		sessionId: "s-b",
		assetId: "device/b",
		assetKind: "device",
		panes: [{ id: "p-b", activeTabId: "t1", tabs: [{ id: "t1", tabId: "backend-t1", kind: "terminal", sessionId: "s-b", title: "B terminal", closable: true }] }],
		activePaneId: "p-b",
	} as import("../app/store").Workspace;
	useUi.setState({ workspaces: [workspaceB], activeWorkspaceId: "ws-b" });
	return workspaceB;
}

beforeEach(() => {
	vi.useRealTimers();
	vi.clearAllMocks();
	seed();
	mocks.closeTab.mockImplementation((id: string) => {
		order.push(`close:${id}:${getCsrfToken()}`);
		return Promise.resolve(true);
	});
	mocks.terminalCloseTab.mockImplementation((id: string) => {
		order.push(`close:${id}:${getCsrfToken()}`);
		return Promise.resolve();
	});
	mocks.disconnect.mockImplementation((id: string) => {
		order.push(`disconnect:${id}:${getCsrfToken()}`);
		return Promise.resolve();
	});
	mocks.logout.mockImplementation(() => {
		order.push(`logout:${getCsrfToken()}`);
		return Promise.resolve({ ok: true });
	});
	mocks.logoutAll.mockImplementation(() => {
		order.push(`logout-all:${getCsrfToken()}`);
		return Promise.resolve({ revoked: 1 });
	});
	mocks.login.mockImplementation(() => {
		order.push(`login:${getCsrfToken()}`);
		return Promise.resolve({
			user: { ...USER, display_name: "Alice next" },
			csrf_token: "csrf-b",
			mfa_required: false,
		});
	});
	mocks.dekGet.mockResolvedValue({
		dek_envelope: "",
		kdf_salt: "",
		kdf_params: "{}",
		recovery_envelope: "",
		recovery_hash: "",
	});
});

afterEach(() => {
	vi.useRealTimers();
	setCsrfToken(null);
	useAuth.setState({ user: null, dek: null, gate: "loading", error: null, cleanupError: null });
	useUi.setState({ workspaces: [], sessions: [], activeWorkspaceId: null, toasts: [] });
});

describe("auth logout device cleanup", () => {
	it("keeps the old credential valid until cleanup finishes before active logout", async () => {
		await useAuth.getState().logout();

		expect(order).toEqual([
			"close:t1:csrf-a",
			"disconnect:s-dev:csrf-a",
			"logout:csrf-a",
		]);
		expect(mocks.closeTab).toHaveBeenCalledTimes(1);
		expect(mocks.disconnect).toHaveBeenCalledTimes(1);
		expect(mocks.logout).toHaveBeenCalledTimes(1);
		expect(mocks.logoutAll).not.toHaveBeenCalled();
		expect(useAuth.getState().user).toBeNull();
		expect(useAuth.getState().gate).toBe("login");
	});

	it("uses the same pre-revocation cleanup for logout all", async () => {
		await useAuth.getState().logout({ all: true });

		expect(order).toEqual([
			"close:t1:csrf-a",
			"disconnect:s-dev:csrf-a",
			"logout-all:csrf-a",
		]);
		expect(mocks.logout).not.toHaveBeenCalled();
		expect(mocks.logoutAll).toHaveBeenCalledTimes(1);
	});

	it("cleans and revokes A before adopting B even when user ID and username are unchanged", async () => {
		await useAuth.getState().login("alice", "password");

		expect(order).toEqual([
			"close:t1:csrf-a",
			"disconnect:s-dev:csrf-a",
			"logout:csrf-a",
			"login:null",
		]);
		expect(mocks.closeTab).toHaveBeenCalledTimes(1);
		expect(mocks.disconnect).toHaveBeenCalledTimes(1);
		expect(mocks.logout).toHaveBeenCalledTimes(1);
		expect(mocks.login).toHaveBeenCalledTimes(1);
		expect(useAuth.getState().user?.id).toBe(USER.id);
		expect(useAuth.getState().user?.username).toBe(USER.username);
		expect(useAuth.getState().user?.display_name).toBe("Alice next");
		expect(useAuth.getState().gate).toBe("ready");
		expect(getCsrfToken()).toBe("csrf-b");
		expect(useUi.getState().workspaces).toEqual([]);
	});

	it("bounds active logout cleanup and retains every timed-out operation", async () => {
		vi.useFakeTimers();
		mocks.closeTab.mockImplementation((id: string) => {
			order.push(`close:${id}:${getCsrfToken()}`);
			return new Promise<boolean>(() => {});
		});
		mocks.disconnect.mockImplementation((id: string) => {
			order.push(`disconnect:${id}:${getCsrfToken()}`);
			return new Promise<void>(() => {});
		});
		const logout = useAuth.getState().logout();
		await vi.advanceTimersByTimeAsync(DEVICE_SESSION_CLEANUP_TIMEOUT_MS);
		await logout;

		expect(order).toEqual([
			"close:t1:csrf-a",
			"disconnect:s-dev:csrf-a",
			"logout:csrf-a",
		]);
		expect(mocks.logout).toHaveBeenCalledTimes(1);
		expect(useAuth.getState().gate).toBe("login");
		expect(useAuth.getState().error?.code).toBe("cleanup_failed");
		expect(useAuth.getState().error?.message).toContain("t1");
		expect(useAuth.getState().error?.message).toContain("s-dev");
		expect(useAuth.getState().error?.message).toContain("8 秒");
		expect(useAuth.getState().error?.message).toContain("补充清理");
		expect(useUi.getState().toasts.some((toast) =>
			toast.text.includes("t1") && toast.text.includes("s-dev") && toast.text.includes("8 秒")
		)).toBe(true);
	});

	it("continues A to B after a backend cleanup timeout and ignores its late result", async () => {
		vi.useFakeTimers();
		useUi.setState((current) => ({
			workspaces: current.workspaces.map((workspace) => ({
				...workspace,
				panes: workspace.panes.map((pane) => ({
					...pane,
					tabs: pane.tabs.map((tab) => ({ ...tab, tabId: "backend-t1" })),
				})),
			})),
		}));
		let finishClose!: () => void;
		mocks.terminalCloseTab.mockImplementation((id: string) => {
			order.push(`close:${id}:${getCsrfToken()}`);
			return new Promise<void>((resolve) => {
				finishClose = resolve;
			});
		});
		const switchAccount = useAuth.getState().login("alice", "password");
		await vi.advanceTimersByTimeAsync(DEVICE_SESSION_CLEANUP_TIMEOUT_MS);
		await switchAccount;

		expect(order).toEqual([
			"close:backend-t1:csrf-a",
			"disconnect:s-dev:csrf-a",
			"logout:csrf-a",
			"login:null",
		]);
		expect(mocks.closeTab).not.toHaveBeenCalled();
		expect(mocks.terminalCloseTab).toHaveBeenCalledWith("backend-t1", "kill", "s-dev");
		expect(mocks.logout).toHaveBeenCalledTimes(1);
		expect(mocks.login).toHaveBeenCalledTimes(1);
		expect(useAuth.getState().gate).toBe("ready");
		expect(useAuth.getState().user?.display_name).toBe("Alice next");
		const authError = useAuth.getState().error;
		expect(authError?.code).toBe("cleanup_failed");
		expect(authError?.message).toContain("t1");
		expect(authError?.message).toContain("8 秒");
		expect(authError?.message).toContain("补充清理");
		expect(useAuth.getState().cleanupError).toEqual(authError);
		expect(useUi.getState().toasts.some((toast) => toast.text.includes("t1") && toast.text.includes("8 秒"))).toBe(true);

		const workspaceB = {
			id: "ws-b",
			kind: "session",
			title: "B device",
			closable: true,
			sessionId: "s-b",
			assetId: "device/b",
			assetKind: "device",
			panes: [{ id: "p-b", activeTabId: "t1", tabs: [{ id: "t1", tabId: "backend-t1", kind: "terminal", sessionId: "s-b", title: "B terminal", closable: true }] }],
			activePaneId: "p-b",
		} as never;
		useUi.setState({ workspaces: [workspaceB], activeWorkspaceId: "ws-b" });
		finishClose();
		await Promise.resolve();
		await Promise.resolve();
		expect(useUi.getState().workspaces).toEqual([workspaceB]);
		expect(useUi.getState().activeWorkspaceId).toBe("ws-b");
	});

	it("does not send revocation or clear B when the epoch changes during cleanup", async () => {
		let finishClose!: (ok: boolean) => void;
		let finishDisconnect!: () => void;
		mocks.closeTab.mockImplementation((id: string) => {
			order.push(`close:${id}:${getCsrfToken()}`);
			return new Promise<boolean>((resolve) => {
				finishClose = resolve;
			});
		});
		mocks.disconnect.mockImplementation((id: string) => {
			order.push(`disconnect:${id}:${getCsrfToken()}`);
			return new Promise<void>((resolve) => {
				finishDisconnect = resolve;
			});
		});
		const logout = useAuth.getState().logout();
		await vi.waitFor(() => {
			expect(mocks.closeTab).toHaveBeenCalledTimes(1);
			expect(mocks.disconnect).toHaveBeenCalledTimes(1);
		});
		const workspaceB = adoptB();
		finishClose(true);
		finishDisconnect();
		await logout;

		expect(mocks.logout).not.toHaveBeenCalled();
		expect(useAuth.getState().user?.display_name).toBe("Alice next");
		expect(useAuth.getState().gate).toBe("ready");
		expect(getCsrfToken()).toBe("csrf-b");
		expect(useUi.getState().workspaces).toEqual([workspaceB]);
		expect(useUi.getState().activeWorkspaceId).toBe("ws-b");
	});

	it("does not clear B when the epoch changes after revocation was sent", async () => {
		let finishLogout!: (result: { ok: boolean }) => void;
		mocks.logout.mockImplementation(() => {
			order.push(`logout:${getCsrfToken()}`);
			return new Promise<{ ok: boolean }>((resolve) => {
				finishLogout = resolve;
			});
		});
		const logout = useAuth.getState().logout();
		await vi.waitFor(() => expect(mocks.logout).toHaveBeenCalledTimes(1));
		const workspaceB = adoptB();
		finishLogout({ ok: true });
		await logout;

		expect(mocks.logout).toHaveBeenCalledTimes(1);
		expect(useAuth.getState().user?.display_name).toBe("Alice next");
		expect(useAuth.getState().gate).toBe("ready");
		expect(getCsrfToken()).toBe("csrf-b");
		expect(useUi.getState().workspaces).toEqual([workspaceB]);
		expect(useUi.getState().activeWorkspaceId).toBe("ws-b");
	});
});
