import { pruneBroadcastToWorkspaces, useUi, type AppTab, type Workspace } from "../../app/store";
import { dbApi, sessionApi, terminalApi } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";

export const DEVICE_SESSION_CLEANUP_TIMEOUT_MS = 8000;

export interface DeviceSessionCleanupFailure {
	operation: "kill" | "disconnect";
	id: string;
	error?: unknown;
}

type CleanupResult = DeviceSessionCleanupFailure | null;

interface CleanupOperation {
	operation: "kill" | "disconnect";
	id: string;
	promise: Promise<CleanupResult>;
	cancel: () => void;
}

interface TrackedOperation extends CleanupOperation {
	settled: boolean;
	result: CleanupResult;
}

type CleanupState = ReturnType<typeof useUi.getState>;

const tabCleanups = new Map<string, CleanupOperation>();
const sessionCleanups = new Map<string, CleanupOperation>();

function closeBackendTab(st: CleanupState, workspace: Workspace, tab: AppTab): Promise<boolean> {
	if (tab.tabId) {
		const kind = st.sessions.find((session) => session.id === tab.sessionId)?.kind ?? workspace.assetKind;
		const remote = !tab.containerId && (kind === "ssh" || kind === "device");
		const request = remote
			? terminalApi.closeTab(tab.tabId, "kill", tab.sessionId)
			: terminalApi.closeTab(tab.tabId, "kill");
		return request.then(() => true).catch((error: unknown) => {
			const code = (error as { code?: string } | null)?.code;
			const ended = tab.kind === "terminal" && (
				code === "not_found" || (code === "disconnected" && (tab.dead || tab.exited))
			);
			if (ended) return true;
			throw error;
		});
	}
	if (tab.kind === "db" && tab.connId) return dbApi.disconnect(tab.connId).then(() => true);
	return useUi.getState().closeTab(tab.id, "kill");
}

function cleanupTab(st: CleanupState, workspace: Workspace, tab: AppTab): CleanupOperation {
	const pending = tabCleanups.get(tab.id);
	if (pending) return pending;
	let promise: Promise<CleanupResult>;
	try {
		promise = Promise.resolve(closeBackendTab(st, workspace, tab)).then(
			(ok) => ok ? null : { operation: "kill", id: tab.id },
			(error: unknown) => ({ operation: "kill", id: tab.id, error }),
		);
	} catch (error) {
		promise = Promise.resolve({ operation: "kill", id: tab.id, error });
	}
	const operation: CleanupOperation = {
		operation: "kill",
		id: tab.id,
		promise,
		cancel: () => {
			if (tabCleanups.get(tab.id) === operation) tabCleanups.delete(tab.id);
		},
	};
	tabCleanups.set(tab.id, operation);
	void promise.then(() => {
		if (tabCleanups.get(tab.id) === operation) tabCleanups.delete(tab.id);
	});
	return operation;
}

function cleanupSession(id: string): CleanupOperation {
	const pending = sessionCleanups.get(id);
	if (pending) return pending;
	let promise: Promise<CleanupResult>;
	try {
		promise = Promise.resolve(sessionApi.disconnect(id)).then(
			() => null,
			(error: unknown) => ({ operation: "disconnect", id, error }),
		);
	} catch (error) {
		promise = Promise.resolve({ operation: "disconnect", id, error });
	}
	const operation: CleanupOperation = {
		operation: "disconnect",
		id,
		promise,
		cancel: () => {
			if (sessionCleanups.get(id) === operation) sessionCleanups.delete(id);
		},
	};
	sessionCleanups.set(id, operation);
	void promise.then(() => {
		if (sessionCleanups.get(id) === operation) sessionCleanups.delete(id);
	});
	return operation;
}

function trackOperation(operation: CleanupOperation): TrackedOperation {
	const tracked: TrackedOperation = { ...operation, settled: false, result: null };
	tracked.promise = operation.promise.then((result) => {
		tracked.settled = true;
		tracked.result = result;
		return result;
	});
	return tracked;
}

function timeoutFailures(operations: TrackedOperation[]): DeviceSessionCleanupFailure[] {
	return operations.map((operation) => {
		if (operation.settled) return operation.result;
		operation.cancel();
		return {
			operation: operation.operation,
			id: operation.id,
			error: { code: "timeout", message: `关闭设备连接超过 ${DEVICE_SESSION_CLEANUP_TIMEOUT_MS / 1000} 秒仍未完成` },
		};
	}).filter((result): result is DeviceSessionCleanupFailure => result !== null);
}

export function cleanupDeviceSessions(): Promise<DeviceSessionCleanupFailure[]> {
	const st = useUi.getState();
	const deviceWorkspaces = st.workspaces.filter((w) => w.assetKind === "device");
	if (deviceWorkspaces.length === 0) return Promise.resolve([]);
	const tabTargets = deviceWorkspaces.flatMap((workspace) =>
		workspace.panes.flatMap((pane) => pane.tabs.map((tab) => ({ workspace, tab })))
	);
	const tabOperations = [...new Map(tabTargets.map((target) => [target.tab.id, target] as const)).values()]
		.map(({ workspace, tab }) => cleanupTab(st, workspace, tab));
	const sessionIDs = [...new Set(deviceWorkspaces.map((w) => w.sessionId).filter((id): id is string => Boolean(id)))];
	const operations = [...tabOperations, ...sessionIDs.map(cleanupSession)].map(trackOperation);
	const removed = new Set(deviceWorkspaces.map((w) => w.id));
	useUi.setState((current) => {
		const remaining = current.workspaces.filter((w) => !removed.has(w.id));
		return {
			workspaces: remaining,
			...(current.broadcast
				? { broadcast: pruneBroadcastToWorkspaces(current.broadcast, remaining) }
				: {}),
			activeWorkspaceId: current.activeWorkspaceId && removed.has(current.activeWorkspaceId)
				? (remaining.at(-1)?.id ?? null)
				: current.activeWorkspaceId,
		};
	});
	// The IPC API has no abort signal. Bound the wait, detach timed-out IDs, and let server fallback retry them.
	const completion = Promise.all(operations.map((operation) => operation.promise)).then((results) =>
		results.filter((result): result is DeviceSessionCleanupFailure => result !== null)
	);
	let timer: ReturnType<typeof setTimeout> | undefined;
	const timeout = new Promise<DeviceSessionCleanupFailure[]>((resolve) => {
		timer = setTimeout(() => resolve(timeoutFailures(operations)), DEVICE_SESSION_CLEANUP_TIMEOUT_MS);
	});
	return Promise.race([completion, timeout]).then((failures) => {
		if (timer !== undefined) clearTimeout(timer);
		return failures;
	});
}

export function deviceSessionCleanupMessage(failures: DeviceSessionCleanupFailure[]): string {
	const details = failures.map((failure) => {
		const operation = failure.operation === "kill" ? `终端 ${failure.id} 关闭失败` : `会话 ${failure.id} 断开失败`;
		return failure.error === undefined ? operation : `${operation}（${describeError(failure.error)}）`;
	});
	return `设备连接未能全部关闭：${details.join("；")}`;
}

export function reportDeviceSessionCleanup(failures: DeviceSessionCleanupFailure[]): void {
	if (failures.length > 0) useUi.getState().pushToast("error", deviceSessionCleanupMessage(failures));
}
