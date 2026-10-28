/** @vitest-environment jsdom */
import { act, createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flush, mount, type MountedView } from "./features/reactTestUtils";
import type { AccountUser } from "../ipc/authApi";

const mocks = vi.hoisted(() => {
	(window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
	return {
		devices: vi.fn(),
		sessions: vi.fn(),
		totpStatus: vi.fn(),
	};
});

vi.mock("../ipc/authApi", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../ipc/authApi")>();
	return {
		...actual,
		authApi: {
			...actual.authApi,
			devices: mocks.devices,
			sessions: mocks.sessions,
			totpStatus: mocks.totpStatus,
		},
	};
});

import { AuthCard } from "../features/settings/AuthCard";
import { useAuth } from "../features/auth/store";

const USER_B: AccountUser = {
	id: "u-b",
	username: "bob",
	display_name: "Bob",
	role: "user",
	state: "active",
	must_change_password: false,
	mfa_enabled: false,
	created_at: 1,
	updated_at: 1,
	last_login_at: 1,
};
const CLEANUP_ERROR = {
	code: "cleanup_failed",
	message: "已退出登录；设备清理未完成：终端 t1 关闭失败（超时：等待超过 8 秒）。服务端已收到退出请求，并将按原会话补充清理。",
};

let mounted: MountedView | null = null;
let client: QueryClient | null = null;

function mountCard(): void {
	client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	mounted = mount(createElement(QueryClientProvider, { client }, createElement(AuthCard)));
}

beforeEach(() => {
	vi.clearAllMocks();
	document.body.replaceChildren();
	mocks.devices.mockResolvedValue({ devices: [] });
	mocks.sessions.mockResolvedValue({ sessions: [] });
	mocks.totpStatus.mockResolvedValue({ enabled: false, pending: false, mfa_required: false, recovery_codes_left: 0 });
	useAuth.setState({
		status: { initialized: true, registration_open: false, auth: "on" },
		user: USER_B,
		dek: new Uint8Array(32).fill(5),
		gate: "ready",
		error: CLEANUP_ERROR,
		cleanupError: CLEANUP_ERROR,
	});
});

afterEach(() => {
	mounted?.unmount();
	mounted = null;
	client?.clear();
	client = null;
	useAuth.setState({ user: null, dek: null, gate: "loading", error: null, cleanupError: null });
});

describe("AuthCard cleanup evidence", () => {
	it("shows cleanup failure and fallback details in ready state and dismisses them explicitly", async () => {
		mountCard();
		await flush();
		const alert = mounted!.container.querySelector("[role=alert]");
		expect(alert?.textContent).toContain("设备清理未完成");
		expect(alert?.textContent).toContain("t1");
		expect(alert?.textContent).toContain("8 秒");
		expect(alert?.textContent).toContain("补充清理");

		clickButton(mounted!.container, "知道了");
		await flush();
		expect(mounted!.container.querySelector("[role=alert]")).toBeNull();
		expect(useAuth.getState().error).toBeNull();
		expect(useAuth.getState().cleanupError).toBeNull();
	});

	it("prioritizes a newer auth error and returns to cleanup evidence after it is acknowledged", async () => {
		mountCard();
		await flush();
		act(() => {
			useAuth.setState({ error: { code: "forbidden", message: "新登录失败" } });
		});
		await flush();
		let alert = mounted!.container.querySelector("[role=alert]");
		expect(alert?.textContent).toContain("新登录失败");
		expect(alert?.textContent).not.toContain("补充清理");

		clickButton(mounted!.container, "知道了");
		await flush();
		expect(useAuth.getState().error).toBeNull();
		alert = mounted!.container.querySelector("[role=alert]");
		expect(alert?.textContent).toContain("设备清理未完成");
		expect(alert?.textContent).toContain("补充清理");
		expect(useAuth.getState().cleanupError).toEqual(CLEANUP_ERROR);
	});
});
