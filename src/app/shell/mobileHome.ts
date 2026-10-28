import { create } from "zustand";

// The mobile shell's home: the status wall covers the workspaces while open. The workspaces stay mounted underneath,
// so terminals keep their connections and scrollback. Only the mobile shell ever opens it.
interface MobileHomeState {
  open: boolean;
}

export const useMobileHome = create<MobileHomeState>(() => ({ open: false }));

let shownOnLaunch = false;

// showMobileHomeOnLaunch opens the home once per app run, when the mobile shell first mounts.
export function showMobileHomeOnLaunch(): void {
  if (shownOnLaunch) return;
  shownOnLaunch = true;
  showMobileHome();
}

export function showMobileHome(): void {
  if (!useMobileHome.getState().open) useMobileHome.setState({ open: true });
}

export function hideMobileHome(): void {
  if (useMobileHome.getState().open) useMobileHome.setState({ open: false });
}

export function isMobileHomeOpen(): boolean {
  return useMobileHome.getState().open;
}

export function resetMobileHomeForTest(): void {
  shownOnLaunch = false;
  useMobileHome.setState({ open: false });
}
