/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AccountUser } from "../ipc/authApi";

vi.mock("../features/auth/crypto", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../features/auth/crypto")>();
	return { ...actual, unwrapDEKWithPassword: vi.fn().mockResolvedValue(new Uint8Array(32).fill(6)) };
});

import { useAuth } from "../features/auth/store";
import { useUi } from "../app/store";
import {
	authApi,
	getCsrfToken,
	SESSION_EXPIRED_EVENT,
	SESSION_IDENTITY_CHANGED_EVENT,
	setCsrfToken,
} from "../ipc/authApi";

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
let identityChanged: import("vitest").Mock<() => void>;

beforeEach(() => {
	vi.clearAllMocks();
	sessionStorage.clear();
	(window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
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
	expired = vi.fn();
	identityChanged = vi.fn();
	window.addEventListener(SESSION_EXPIRED_EVENT, expired);
	window.addEventListener(SESSION_IDENTITY_CHANGED_EVENT, identityChanged);
});

afterEach(() => {
	window.removeEventListener(SESSION_EXPIRED_EVENT, expired);
	window.removeEventListener(SESSION_IDENTITY_CHANGED_EVENT, identityChanged);
	setCsrfToken(null);
	useAuth.setState({ user: null, dek: null, gate: "loading", error: null, cleanupError: null });
	useUi.setState({ workspaces: [], sessions: [], activeWorkspaceId: null, toasts: [] });
	vi.unstubAllGlobals();
});

function jsonResponse(status: number, body: unknown): Response {
	return {
		ok: status >= 200 && status < 300,
		status,
		text: async () => JSON.stringify(body),
	} as Response;
}

async function expectLateMeIsolated(status: number, throughRefresh = false): Promise<void> {
	let respond!: (response: Response) => void;
	const meResponse = new Promise<Response>((resolve) => {
		respond = resolve;
	});
	const calls: string[] = [];
	const fetchMock = vi.fn((input: string) => {
		const url = String(input);
		calls.push(url);
		if (url === "/auth/status") {
			return Promise.resolve(jsonResponse(200, { initialized: true, registration_open: false, auth: "on" }));
		}
		if (url === "/auth/me") return meResponse;
		if (url === "/auth/logout") return Promise.resolve(jsonResponse(200, { ok: true }));
		if (url === "/auth/login") {
			return Promise.resolve(jsonResponse(200, { user: USER_B, csrf_token: "csrf-b", mfa_required: false }));
		}
		if (url === "/auth/dek") {
			return Promise.resolve(jsonResponse(200, {
				dek_envelope: "",
				kdf_salt: "",
				kdf_params: "{}",
				recovery_envelope: "",
				recovery_hash: "",
			}));
		}
		return Promise.reject(new Error(`unexpected fetch: ${url}`));
	});
	vi.stubGlobal("fetch", fetchMock);
	const meA = throughRefresh ? useAuth.getState().refresh() : authApi.me();
	await vi.waitFor(() => expect(calls).toContain("/auth/me"));

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

	respond(jsonResponse(status, status === 200
		? { user: USER_A, csrf_token: "csrf-a-late", mfa_required: false }
		: { error: { code: "forbidden", message: "session expired" } }));
	if (throughRefresh) await meA;
	else await expect(meA).rejects.toMatchObject({ code: "account_changed" });
	expect(expired).not.toHaveBeenCalled();
	expect(identityChanged).not.toHaveBeenCalled();
	expect(useAuth.getState().user?.id).toBe(USER_B.id);
	expect(useAuth.getState().gate).toBe("ready");
	expect(getCsrfToken()).toBe("csrf-b");
	expect(useUi.getState().workspaces).toEqual([workspaceB]);
	expect(useUi.getState().activeWorkspaceId).toBe("ws-b");
}

describe("authApi account epoch isolation", () => {
	it("does not restore A identity or CSRF when me returns a late 200 after B password login", async () => {
		await expectLateMeIsolated(200);
	});

	it("does not clear B when me returns a late 401 after B password login", async () => {
		await expectLateMeIsolated(401);
	});

	it("refresh ignores a late account_changed instead of clearing B", async () => {
		await expectLateMeIsolated(200, true);
	});

	it("does not finalize an old logout over a newly adopted B session", async () => {
		let respond!: (response: Response) => void;
		const logoutResponse = new Promise<Response>((resolve) => {
			respond = resolve;
		});
		const fetchMock = vi.fn(() => logoutResponse);
		vi.stubGlobal("fetch", fetchMock);
		const logoutA = useAuth.getState().logout();
		await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
		setCsrfToken("csrf-b");
		useAuth.setState({ user: USER_B, gate: "ready" });

		respond(jsonResponse(200, { ok: true }));
		await logoutA;
		expect(useAuth.getState().user?.id).toBe(USER_B.id);
		expect(useAuth.getState().gate).toBe("ready");
		expect(getCsrfToken()).toBe("csrf-b");
		expect(expired).not.toHaveBeenCalled();
	});
});
