/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AccountUser } from "../ipc/authApi";

const mocks = vi.hoisted(() => {
	(window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
	return {
		login: vi.fn(),
		logout: vi.fn(),
		dekGet: vi.fn(),
	};
});

vi.mock("../ipc/authApi", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../ipc/authApi")>();
	return {
		...actual,
		authApi: {
			...actual.authApi,
			login: mocks.login,
			logout: mocks.logout,
			dekGet: mocks.dekGet,
		},
	};
});

vi.mock("../features/auth/crypto", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../features/auth/crypto")>();
	return { ...actual, unwrapDEKWithPassword: vi.fn().mockResolvedValue(new Uint8Array(32).fill(9)) };
});

import { useAuth } from "../features/auth/store";
import { useUi } from "../app/store";
import { getCsrfToken, SESSION_EXPIRED_EVENT, setCsrfToken } from "../ipc/authApi";
import { terminalApi } from "../ipc/commands";

const USER_A: AccountUser = {
	id: "u-a",
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
const USER_B: AccountUser = { ...USER_A, id: "u-b", username: "bob", display_name: "Bob" };

let expired: import("vitest").Mock<() => void>;

beforeEach(() => {
	vi.clearAllMocks();
	setCsrfToken(null);
	useAuth.setState({
		status: { initialized: true, registration_open: false, auth: "on" },
		user: USER_A,
		dek: null,
		gate: "ready",
		error: null,
		cleanupError: null,
	});
	setCsrfToken("csrf-a");
	useUi.setState({ workspaces: [], sessions: [], activeWorkspaceId: null, toasts: [] });
	mocks.logout.mockResolvedValue({ ok: true });
	mocks.login.mockResolvedValue({ user: USER_B, csrf_token: "csrf-b", mfa_required: false });
	mocks.dekGet.mockResolvedValue({
		dek_envelope: "",
		kdf_salt: "",
		kdf_params: "{}",
		recovery_envelope: "",
		recovery_hash: "",
	});
	expired = vi.fn();
	window.addEventListener(SESSION_EXPIRED_EVENT, expired);
});

afterEach(() => {
	window.removeEventListener(SESSION_EXPIRED_EVENT, expired);
	setCsrfToken(null);
	useAuth.setState({ user: null, dek: null, gate: "loading", error: null, cleanupError: null });
	useUi.setState({ workspaces: [], sessions: [], activeWorkspaceId: null, toasts: [] });
	vi.unstubAllGlobals();
});

function rpcResponse(status: number): Response {
	return {
		status,
		text: async () => JSON.stringify(status === 200
			? { ok: true, data: null }
			: { ok: false, error: { code: "forbidden", message: "session expired" } }),
	} as Response;
}

async function expectLateResponseIsolated(status: number): Promise<void> {
	let respond!: (response: Response) => void;
	const response = new Promise<Response>((resolve) => {
		respond = resolve;
	});
	const fetchMock = vi.fn(() => response);
	vi.stubGlobal("fetch", fetchMock);
	const requestA = terminalApi.closeTab("backend-a", "kill", "session-a");
	await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

	await useAuth.getState().login("bob", "password");
	expect(useAuth.getState().user?.id).toBe(USER_B.id);
	expect(useAuth.getState().gate).toBe("ready");
	expect(getCsrfToken()).toBe("csrf-b");
	const workspaceB = {
		id: "ws-b",
		kind: "session",
		title: "B device",
		closable: true,
		sessionId: "s-b",
		assetId: "device/b",
		assetKind: "device",
		panes: [{ id: "p-b", activeTabId: "t-b", tabs: [{ id: "t-b", tabId: "backend-b", kind: "terminal", sessionId: "s-b", title: "B terminal", closable: true }] }],
		activePaneId: "p-b",
		splitRatio: 0.5,
	} as never;
	useUi.setState({ workspaces: [workspaceB], activeWorkspaceId: "ws-b" });

	respond(rpcResponse(status));
	await expect(requestA).rejects.toMatchObject({ code: "account_changed" });
	expect(expired).not.toHaveBeenCalled();
	expect(useAuth.getState().user?.id).toBe(USER_B.id);
	expect(useAuth.getState().gate).toBe("ready");
	expect(getCsrfToken()).toBe("csrf-b");
	expect(useUi.getState().workspaces).toEqual([workspaceB]);
	expect(useUi.getState().activeWorkspaceId).toBe("ws-b");
}

describe("web RPC account epoch isolation", () => {
	it("does not clear B when A returns a late 401", async () => {
		await expectLateResponseIsolated(401);
	});

	it("does not apply a late 200 from A to B", async () => {
		await expectLateResponseIsolated(200);
	});
});
