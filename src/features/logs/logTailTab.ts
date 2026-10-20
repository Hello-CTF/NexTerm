export const MULTI_HOST_LOG_TAB_ID = "multi-host-log-tail";

const MULTI_HOST_LOG_TAB_PREFIX = `${MULTI_HOST_LOG_TAB_ID}:`;

export function createMultiHostLogTabId(): string {
  return `${MULTI_HOST_LOG_TAB_PREFIX}${globalThis.crypto.randomUUID()}`;
}

export function isMultiHostLogTabId(id: string): boolean {
  return id === MULTI_HOST_LOG_TAB_ID || id.startsWith(MULTI_HOST_LOG_TAB_PREFIX);
}
