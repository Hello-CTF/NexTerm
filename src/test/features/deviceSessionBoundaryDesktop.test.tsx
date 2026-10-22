/** @vitest-environment jsdom */
import { act, createElement, Fragment } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { deferred, flush, flushUntil, mount, type MountedView } from "./reactTestUtils";
import type { AccountUser } from "../../ipc/authApi";
import type { SyncStatus } from "../../ipc/types";

const mocks = vi.hoisted(() => {
	(window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
	return {
		status: vi.fn(),
		linkGet: vi.fn(),
		me: vi.fn(),
		devices: vi.fn(),
		disconnect: vi.fn((_sessionId: string) => Promise.resolve()),
		closeTab: vi.fn((_id: string, _mode?: "kill" | "detach") => Promise.resolve(true)),
	};
});

vi.mock("../../ipc/commands", async (importOriginal) => {
	const { createCommandsMock } = await import("../helpers/mockCommands");
	return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
		syncApi: { status: mocks.status, linkGet: mocks.linkGet },
		sessionApi: { disconnect: mocks.disconnect },
	});
});

vi.mock("../../ipc/remoteApi", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../../ipc/remoteApi")>();
	return {
		...actual,
		remoteAccountApi: { ...actual.remoteAccountApi, me: mocks.me, devices: mocks.devices },
	};
});

import { DeviceSessionBoundary } from "../../features/fleet/DeviceSessionBoundary";
import { AuthCard } from "../../features/settings/AuthCard";
import { useUi } from "../../app/store";

const SYNC_LINK_KEY = ["sync-link"];
const IDENTITY_KEY = ["sync-link", "identity"];
const ACCOUNT_SCOPE_KEY = ["sync-link", "account-scope-link"];
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
const OTHER_USER: AccountUser = { ...USER, id: "u-2", username: "bob", display_name: "Bob" };

let mounted: MountedView | null = null;
let client: QueryClient | null = null;

function syncStatus(user: AccountUser | null): SyncStatus {
	return {
		configured: Boolean(user),
		loggedIn: Boolean(user),
		...(user ? { username: user.username, userId: user.id } : {}),
		head: "head-1",
		seq: 7,
		verifiedAt: 1,
		lastError: "",
	};
}

function accountLink(user: AccountUser, url = "https://server-a.example") {
	return {
		url,
		username: user.username,
		insecure: false,
		hasPassword: true,
		verifiedAt: 1,
		lastError: "",
	};
}

function tree(withSettings: boolean) {
	return createElement(
		QueryClientProvider,
		{ client: client! },
		createElement(
			Fragment,
			null,
			createElement(DeviceSessionBoundary),
			withSettings ? createElement(AuthCard) : null,
		),
	);
}

function mountBoundary(withSettings = false): QueryClient {
	client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	mounted = mount(tree(withSettings));
	return client;
}

function beginInvalidation(): Promise<void> {
	let invalidation!: Promise<void>;
	act(() => {
		invalidation = client!.invalidateQueries({ queryKey: SYNC_LINK_KEY });
	});
	return invalidation;
}

async function settleIdentity(): Promise<void> {
	await flushUntil(() => {
		const identity = client!.getQueryState(IDENTITY_KEY);
		const scope = client!.getQueryState(ACCOUNT_SCOPE_KEY);
		const user = client!.getQueryData<AccountUser | null>(IDENTITY_KEY);
		return identity?.status === "success" && identity.fetchStatus === "idle" &&
			(!user || (scope?.status === "success" && scope.fetchStatus === "idle"));
	});
	await flush();
}

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
				panes: [{ id: "p2", activeTabId: "t2", tabs: [{ id: "t2", kind: "terminal", sessionId: "s-ssh", title: "Terminal", closable: true }] }],
				activePaneId: "p2",
				splitRatio: 0.5,
			},
		] as never,
		activeWorkspaceId: "ws-dev",
	});
}

function expectDevicePreserved(): void {
	expect(useUi.getState().workspaces.some((w) => w.assetKind === "device")).toBe(true);
	expect(mocks.closeTab).not.toHaveBeenCalled();
	expect(mocks.disconnect).not.toHaveBeenCalled();
}

beforeEach(() => {
	vi.clearAllMocks();
	mocks.status.mockResolvedValue(syncStatus(USER));
	mocks.me.mockResolvedValue({ user: USER });
	mocks.linkGet.mockResolvedValue(accountLink(USER));
	mocks.devices.mockResolvedValue({ devices: [] });
	seed();
});

afterEach(() => {
	mounted?.unmount();
	mounted = null;
	client?.clear();
	client = null;
	useUi.setState({ workspaces: [], sessions: [], activeWorkspaceId: null });
});

describe("desktop device session identity resolution", () => {
	it("does not clean restored device workspaces during initial null-to-user resolution", async () => {
		const initial = deferred<SyncStatus>();
		mocks.status.mockReturnValue(initial.promise);
		mountBoundary();
		await flush();
		expectDevicePreserved();

		await act(async () => {
			initial.resolve(syncStatus(USER));
		});
		await settleIdentity();
		expectDevicePreserved();
	});

	it("does not clean or advance identity during a deferred same-account refetch", async () => {
		mountBoundary();
		await settleIdentity();
		const next = deferred<SyncStatus>();
		mocks.status.mockImplementationOnce(() => next.promise);
		const invalidation = beginInvalidation();
		await flushUntil(() => mocks.status.mock.calls.length === 2);
		await flush();
		expectDevicePreserved();

		await act(async () => {
			next.resolve(syncStatus(USER));
			await invalidation;
		});
		await settleIdentity();
		expectDevicePreserved();
	});

	it("keeps device workspaces when mounting settings triggers a stale refetch and hides old account data meanwhile", async () => {
		mountBoundary();
		await settleIdentity();
		await act(async () => {
			await client!.invalidateQueries({ queryKey: IDENTITY_KEY, refetchType: "none" });
		});
		const next = deferred<SyncStatus>();
		mocks.status.mockImplementationOnce(() => next.promise);
		act(() => {
			mounted!.root.render(tree(true));
		});
		await flushUntil(() => mocks.status.mock.calls.length === 2);
		await flush();
		expectDevicePreserved();
		expect(mounted!.container.textContent ?? "").not.toContain("alice");

		await act(async () => {
			next.resolve(syncStatus(USER));
		});
		await settleIdentity();
		await flushUntil(() => (mounted!.container.textContent ?? "").includes("alice"));
		expectDevicePreserved();
	});

	it("kills device terminals only after a resolved account A-to-B switch", async () => {
		mountBoundary();
		await settleIdentity();
		const next = deferred<SyncStatus>();
		mocks.status.mockImplementationOnce(() => next.promise);
		mocks.me.mockResolvedValue({ user: OTHER_USER });
		mocks.linkGet.mockResolvedValue(accountLink(OTHER_USER));
		const invalidation = beginInvalidation();
		await flushUntil(() => mocks.status.mock.calls.length === 2);
		await flush();
		expectDevicePreserved();

		await act(async () => {
			next.resolve(syncStatus(OTHER_USER));
			await invalidation;
		});
		await flushUntil(() => useUi.getState().workspaces.every((w) => w.assetKind !== "device"));
		expect(mocks.closeTab).toHaveBeenCalledWith("t1", "kill");
		expect(mocks.disconnect).toHaveBeenCalledWith("s-dev");
		expect(useUi.getState().workspaces.map((w) => w.id)).toEqual(["ws-ssh"]);
	});

	it("kills device terminals only after a resolved logout", async () => {
		mountBoundary();
		await settleIdentity();
		const next = deferred<SyncStatus>();
		mocks.status.mockImplementationOnce(() => next.promise);
		const invalidation = beginInvalidation();
		await flushUntil(() => mocks.status.mock.calls.length === 2);
		await flush();
		expectDevicePreserved();

		await act(async () => {
			next.resolve(syncStatus(null));
			await invalidation;
		});
		await flushUntil(() => useUi.getState().workspaces.every((w) => w.assetKind !== "device"));
		expect(mocks.closeTab).toHaveBeenCalledWith("t1", "kill");
		expect(mocks.disconnect).toHaveBeenCalledWith("s-dev");
	});

	it("ignores a failed refetch and retains the previous identity for the next resolved switch", async () => {
		mountBoundary();
		await settleIdentity();
		const failed = deferred<SyncStatus>();
		mocks.status.mockImplementationOnce(() => failed.promise);
		const invalidation = beginInvalidation();
		await flushUntil(() => mocks.status.mock.calls.length === 2);
		await flush();
		expectDevicePreserved();

		await act(async () => {
			failed.reject(new Error("offline"));
			await invalidation;
		});
		await flushUntil(() => client!.getQueryState(IDENTITY_KEY)?.status === "error");
		await flush();
		expectDevicePreserved();

		mocks.status.mockResolvedValue(syncStatus(OTHER_USER));
		mocks.me.mockResolvedValue({ user: OTHER_USER });
		mocks.linkGet.mockResolvedValue(accountLink(OTHER_USER));
		const nextInvalidation = beginInvalidation();
		await act(async () => {
			await nextInvalidation;
		});
		await flushUntil(() => useUi.getState().workspaces.every((w) => w.assetKind !== "device"));
		expect(mocks.closeTab).toHaveBeenCalledWith("t1", "kill");
		expect(mocks.disconnect).toHaveBeenCalledWith("s-dev");
	});

	it("forms a boundary when only the server URL changes for the same user ID and username", async () => {
		mountBoundary();
		await settleIdentity();
		const next = deferred<SyncStatus>();
		mocks.status.mockImplementationOnce(() => next.promise);
		mocks.linkGet.mockResolvedValue(accountLink(USER, "https://server-b.example"));
		const invalidation = beginInvalidation();
		await flushUntil(() => mocks.status.mock.calls.length === 2);
		await flush();
		expectDevicePreserved();

		await act(async () => {
			next.resolve(syncStatus(USER));
			await invalidation;
		});
		await flushUntil(() => useUi.getState().workspaces.every((w) => w.assetKind !== "device"));
		expect(mocks.closeTab).toHaveBeenCalledTimes(1);
		expect(mocks.closeTab).toHaveBeenCalledWith("t1", "kill");
		expect(mocks.disconnect).toHaveBeenCalledTimes(1);
		expect(mocks.disconnect).toHaveBeenCalledWith("s-dev");
	});

	it("keeps old account content hidden after an identity error until a same-named account is resolved on the new server", async () => {
		mountBoundary(true);
		await settleIdentity();
		await flushUntil(() => (mounted!.container.textContent ?? "").includes("alice"));
		const failed = deferred<SyncStatus>();
		mocks.status.mockImplementationOnce(() => failed.promise);
		mocks.linkGet.mockResolvedValue(accountLink(USER, "https://server-b.example"));
		const invalidation = beginInvalidation();
		await flushUntil(() =>
			mocks.status.mock.calls.length === 2 &&
			client!.getQueryData<{ url: string }>(ACCOUNT_SCOPE_KEY)?.url === "https://server-b.example"
		);
		await flush();
		expectDevicePreserved();
		expect(mounted!.container.textContent ?? "").not.toContain("alice");

		await act(async () => {
			failed.reject(new Error("offline"));
			await invalidation;
		});
		await flushUntil(() => client!.getQueryState(IDENTITY_KEY)?.status === "error");
		await flush();
		expectDevicePreserved();
		expect(mounted!.container.textContent ?? "").not.toContain("alice");
		expect(mounted!.container.textContent ?? "").not.toContain("修改密码");

		const recovered = deferred<SyncStatus>();
		mocks.status.mockImplementationOnce(() => recovered.promise);
		const recovery = beginInvalidation();
		await flushUntil(() => mocks.status.mock.calls.length === 3);
		await flush();
		expectDevicePreserved();
		expect(mounted!.container.textContent ?? "").not.toContain("alice");

		await act(async () => {
			recovered.resolve(syncStatus(USER));
			await recovery;
		});
		await flushUntil(() => useUi.getState().workspaces.every((w) => w.assetKind !== "device"));
		await flushUntil(() => (mounted!.container.textContent ?? "").includes("alice"));
		expect(mocks.closeTab).toHaveBeenCalledTimes(1);
		expect(mocks.closeTab).toHaveBeenCalledWith("t1", "kill");
		expect(mocks.disconnect).toHaveBeenCalledTimes(1);
		expect(mocks.disconnect).toHaveBeenCalledWith("s-dev");
	});
});
