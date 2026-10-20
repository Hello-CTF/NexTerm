export const FLEET_HEALTH_TAB_ID = "fleet-health-overview";

const FLEET_HEALTH_TAB_PREFIX = `${FLEET_HEALTH_TAB_ID}:`;

export function createFleetHealthTabId(): string {
  return `${FLEET_HEALTH_TAB_PREFIX}${globalThis.crypto.randomUUID()}`;
}

export function isFleetHealthTabId(id: string): boolean {
  return id === FLEET_HEALTH_TAB_ID || id.startsWith(FLEET_HEALTH_TAB_PREFIX);
}

export function isFleetHealthTab(tab: { id: string; kind: string }): boolean {
  return tab.kind === "log" && isFleetHealthTabId(tab.id);
}
