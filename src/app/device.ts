// Device detection from what the browser reports. Kept free of imports so modules that tests mock partially
// (platform.ts, preferences.ts) can share it.

export interface DeviceSignals {
  userAgent: string;
  platform?: string;
  maxTouchPoints?: number;
}

export function currentDeviceSignals(): DeviceSignals {
  if (typeof navigator === "undefined") return { userAgent: "" };
  return { userAgent: navigator.userAgent ?? "", platform: navigator.platform ?? "", maxTouchPoints: navigator.maxTouchPoints ?? 0 };
}

// iPadOS Safari and WKWebView report a Mac (a "Macintosh" user agent, platform "MacIntel"); only touch support tells
// them apart, as no Mac has a touch screen.
export function isIPadOS(signals: DeviceSignals): boolean {
  if (/iPad/.test(signals.userAgent)) return true;
  const macLike = /Macintosh/.test(signals.userAgent) || /^Mac/i.test(signals.platform ?? "");
  return macLike && (signals.maxTouchPoints ?? 0) > 1;
}

// A phone or tablet: iPhone, iPad (including the iPadOS Mac user agent) or Android.
export function isMobileDevice(signals: DeviceSignals): boolean {
  return /iPhone|iPad|iPod|Android/i.test(signals.userAgent) || isIPadOS(signals);
}

// An Apple keyboard platform: macOS, iOS or iPadOS. Their hardware keyboards put the primary modifier on Command, so
// shortcuts use Cmd there and Ctrl reaches the terminal.
export function isAppleKeyboardDevice(signals: DeviceSignals): boolean {
  return (
    /Mac|iPhone|iPad|iPod/i.test(signals.platform ?? "") ||
    /Macintosh|iPhone|iPad|iPod/.test(signals.userAgent) ||
    isIPadOS(signals)
  );
}

// Below this shortest screen side (CSS px) a mobile device is a phone; Android uses the same 600dp line for tablets.
export const TABLET_MIN_SHORT_SIDE_PX = 600;

// A phone shows one pane at a time; a tablet keeps the split panes its width allows.
export function isPhoneScreen(screenWidth: number, screenHeight: number): boolean {
  const shortSide = Math.min(screenWidth, screenHeight);
  // An unknown screen keeps the single-pane phone layout.
  if (!Number.isFinite(shortSide) || shortSide <= 0) return true;
  return shortSide < TABLET_MIN_SHORT_SIDE_PX;
}
