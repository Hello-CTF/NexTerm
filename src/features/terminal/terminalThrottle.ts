import type { TerminalThrottledEvent } from "../../ipc/events";

export const THROTTLE_RECOVERED_MS = 4000;
// How long the neutral "resyncing" notice stays after the server dropped
// this view's channel for falling behind.
export const THROTTLE_RESYNC_MS = 4000;

export interface ThrottleState {
  channels: Record<string, number>;
  recoveredAt: number | null;
  resyncAt?: number | null;
}

export interface ThrottleView {
  active: boolean;
  recovered: boolean;
  resyncing?: boolean;
  inflightBytes: number;
}

// own tells whether the event's channel is this view's: only that view
// shows the resync notice; for others the channel just leaves the set.
export function throttleStateFrom(
  current: ThrottleState | null,
  event: TerminalThrottledEvent,
  now: number,
  own = false,
): ThrottleState {
  if (event.resync === true) {
    const channels = { ...(current?.channels ?? {}) };
    delete channels[event.channelId];
    return { channels, recoveredAt: null, resyncAt: own ? now : (current?.resyncAt ?? null) };
  }
  if (event.recovered === true) {
    if (!current || Object.keys(current.channels).length === 0) {
      return current ?? { channels: {}, recoveredAt: null };
    }
    return { channels: {}, recoveredAt: now };
  }
  const channels = { ...(current?.channels ?? {}) };
  channels[event.channelId] = event.inflightBytes;
  return { channels, recoveredAt: null };
}

export function throttleView(state: ThrottleState | null, now: number): ThrottleView | null {
  if (!state) return null;
  const ids = Object.keys(state.channels);
  if (ids.length > 0) {
    const inflightBytes = Math.max(...ids.map((id) => state.channels[id]));
    return { active: true, recovered: false, inflightBytes };
  }
  if (state.resyncAt != null && now - state.resyncAt <= THROTTLE_RESYNC_MS) {
    return { active: false, recovered: false, resyncing: true, inflightBytes: 0 };
  }
  if (state.recoveredAt !== null && now - state.recoveredAt <= THROTTLE_RECOVERED_MS) {
    return { active: false, recovered: true, inflightBytes: 0 };
  }
  return null;
}
