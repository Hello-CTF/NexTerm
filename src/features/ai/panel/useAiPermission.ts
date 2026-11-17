import { useCallback, useEffect, useRef, useState } from "react";
import { type AiPermissionMode, type AiPermissionState, aiApi } from "../../../ipc/commands";
import { describeError } from "../../../ui/errorText";
import { t as tr } from "../../../i18n";

// Every mounted chat panel reloads after any of them saves, so two AI tabs never show different modes.
const permissionListeners = new Set<() => void>();

function notifyPermissionChanged() {
  for (const listener of permissionListeners) listener();
}

type Toast = (kind: "error", message: string) => void;

// useAiPermission keeps the permission that applies to the panel's scope. A mode switch edits the global mode, or
// the host's explicit override when the host has one; an override is created or removed only through
// setHostOverride. Responses are applied only while they are the latest request, so switching terminals while a
// request is in flight never shows another host's mode.
export function useAiPermission(permScope: Record<string, unknown> | undefined, pushToast: Toast) {
  const [permState, setPermState] = useState<AiPermissionState | null>(null);
  const [permError, setPermError] = useState<string | null>(null);
  const requestRef = useRef(0);
  const stateRef = useRef(permState);
  stateRef.current = permState;

  const loadPermission = useCallback(async (): Promise<AiPermissionState | null> => {
    const request = ++requestRef.current;
    try {
      const state = await aiApi.permissionState(permScope);
      if (request !== requestRef.current) return null;
      setPermState(state);
      setPermError(null);
      return state;
    } catch (e) {
      if (request === requestRef.current) setPermError(describeError(e));
      return null;
    }
  }, [permScope]);

  useEffect(() => {
    void loadPermission();
  }, [loadPermission]);

  useEffect(() => {
    const listener = () => void loadPermission();
    permissionListeners.add(listener);
    return () => {
      permissionListeners.delete(listener);
    };
  }, [loadPermission]);

  const save = async (
    optimistic: AiPermissionState | null,
    write: (scope: Record<string, unknown> | undefined) => Promise<AiPermissionState>,
  ) => {
    const scope = permScope;
    const request = ++requestRef.current;
    const previous = stateRef.current;
    if (optimistic) setPermState(optimistic);
    try {
      const next = await write(scope);
      if (request === requestRef.current) setPermState(next);
      notifyPermissionChanged();
    } catch (e) {
      if (request === requestRef.current) setPermState(previous);
      pushToast("error", tr("未能保存权限设置：{value}", { value: describeError(e) }));
    }
  };

  // switchMode changes the mode that applies here: the host's override when it has one, else the global mode.
  const switchMode = (mode: AiPermissionMode) => {
    const current = stateRef.current;
    const hostOnly = current?.source === "host";
    const optimistic = current
      ? { ...current, config: { ...current.config, mode }, ...(hostOnly ? { hostMode: mode } : { globalMode: mode }) }
      : null;
    void save(optimistic, async (scope) => {
      // The danger rules travel with the global configuration; read them fresh so a stale panel never reverts them.
      const latest = await aiApi.permissionState(scope);
      return aiApi.setPermission({ ...latest.config, mode }, scope, hostOnly ? { hostOnly: true } : undefined);
    });
  };

  // setHostOverride gives this host its own mode (starting from the one in force) or makes it follow the global mode.
  const setHostOverride = (enabled: boolean) => {
    const current = stateRef.current;
    if (!current || !permScope) return;
    const optimistic: AiPermissionState = enabled
      ? { ...current, source: "host", hostMode: current.config.mode }
      : { ...current, source: "global", hostMode: undefined, config: { ...current.config, mode: current.globalMode } };
    void save(optimistic, (scope) =>
      aiApi.setPermission(current.config, scope, enabled ? { hostOnly: true } : { followGlobal: true }),
    );
  };

  return { permState, perm: permState?.config ?? null, permError, loadPermission, switchMode, setHostOverride };
}
