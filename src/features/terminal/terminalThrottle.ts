import type { TerminalThrottledEvent } from "../../ipc/events";

export const THROTTLE_RECOVERED_MS = 4000;

export interface ThrottleState {
  channels: Record<string, number>;
  recoveredAt: number | null;
}

export interface ThrottleView {
  active: boolean;
  recovered: boolean;
  inflightBytes: number;
}

export function throttleStateFrom(
  current: ThrottleState | null,
  event: TerminalThrottledEvent,
  now: number,
): ThrottleState {
  const channels = { ...(current?.channels ?? {}) };
  const channelId = event.channelId ?? "";
  if (event.recovered === true) {
    if (!(channelId in channels)) {
      return current ?? { channels, recoveredAt: null };
    }
    delete channels[channelId];
    return {
      channels,
      recoveredAt: Object.keys(channels).length === 0 ? now : null,
    };
  }
  channels[channelId] = event.inflightBytes;
  return { channels, recoveredAt: null };
}

export function throttleView(state: ThrottleState | null, now: number): ThrottleView | null {
  if (!state) return null;
  const ids = Object.keys(state.channels);
  if (ids.length > 0) {
    const inflightBytes = Math.max(...ids.map((id) => state.channels[id]));
    return { active: true, recovered: false, inflightBytes };
  }
  if (state.recoveredAt !== null && now - state.recoveredAt <= THROTTLE_RECOVERED_MS) {
    return { active: false, recovered: true, inflightBytes: 0 };
  }
  return null;
}
