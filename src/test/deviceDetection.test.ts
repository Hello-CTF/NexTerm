import { describe, expect, it } from "vitest";
import { isAppleKeyboardDevice, isIPadOS, isMobileDevice, isPhoneScreen } from "../app/device";
import { singlePaneFor } from "../app/shell/shellMode";

const IPAD_DESKTOP_UA =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15";
const IPAD_WKWEBVIEW_UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko)";
const IPAD_MOBILE_UA =
  "Mozilla/5.0 (iPad; CPU OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1";
const IPHONE_UA =
  "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1";
const ANDROID_UA = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0 Mobile Safari/537.36";
const WINDOWS_UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0 Safari/537.36";

describe("device detection", () => {
  it("recognizes an iPad that reports a Mac by its touch points", () => {
    for (const userAgent of [IPAD_DESKTOP_UA, IPAD_WKWEBVIEW_UA]) {
      const ipad = { userAgent, platform: "MacIntel", maxTouchPoints: 5 };
      expect(isIPadOS(ipad)).toBe(true);
      expect(isMobileDevice(ipad)).toBe(true);
    }
    expect(isIPadOS({ userAgent: IPAD_MOBILE_UA, platform: "iPad", maxTouchPoints: 5 })).toBe(true);
  });

  it("keeps a Mac without a touch screen a desktop", () => {
    const mac = { userAgent: IPAD_DESKTOP_UA, platform: "MacIntel", maxTouchPoints: 0 };
    expect(isIPadOS(mac)).toBe(false);
    expect(isMobileDevice(mac)).toBe(false);
    expect(isMobileDevice({ userAgent: IPAD_DESKTOP_UA })).toBe(false);
    // A touch screen on Windows is not an iPad.
    expect(isMobileDevice({ userAgent: WINDOWS_UA, platform: "Win32", maxTouchPoints: 10 })).toBe(false);
  });

  it("treats phones and Android tablets as mobile devices", () => {
    expect(isMobileDevice({ userAgent: IPHONE_UA, platform: "iPhone", maxTouchPoints: 5 })).toBe(true);
    expect(isMobileDevice({ userAgent: ANDROID_UA, platform: "Linux armv8l", maxTouchPoints: 5 })).toBe(true);
    expect(isIPadOS({ userAgent: IPHONE_UA, platform: "iPhone", maxTouchPoints: 5 })).toBe(false);
  });

  it("puts iPad, iPhone and Mac keyboards on Command and keeps Ctrl elsewhere", () => {
    expect(isAppleKeyboardDevice({ userAgent: IPAD_DESKTOP_UA, platform: "MacIntel", maxTouchPoints: 5 })).toBe(true);
    expect(isAppleKeyboardDevice({ userAgent: IPAD_MOBILE_UA, platform: "iPad", maxTouchPoints: 5 })).toBe(true);
    expect(isAppleKeyboardDevice({ userAgent: IPHONE_UA, platform: "iPhone", maxTouchPoints: 5 })).toBe(true);
    expect(isAppleKeyboardDevice({ userAgent: IPAD_DESKTOP_UA, platform: "MacIntel", maxTouchPoints: 0 })).toBe(true);
    expect(isAppleKeyboardDevice({ userAgent: ANDROID_UA, platform: "Linux armv8l", maxTouchPoints: 5 })).toBe(false);
    expect(isAppleKeyboardDevice({ userAgent: WINDOWS_UA, platform: "Win32", maxTouchPoints: 10 })).toBe(false);
  });
});

describe("phone and tablet layout", () => {
  it("draws the tablet line at a 600 px shortest screen side", () => {
    // iPhone 15 and a large Android phone, in either orientation.
    expect(isPhoneScreen(393, 852)).toBe(true);
    expect(isPhoneScreen(915, 412)).toBe(true);
    // iPad mini, iPad Pro 13 and a 600dp Android tablet.
    expect(isPhoneScreen(744, 1133)).toBe(false);
    expect(isPhoneScreen(1376, 1032)).toBe(false);
    expect(isPhoneScreen(600, 960)).toBe(false);
    // An unknown screen keeps the phone layout.
    expect(isPhoneScreen(0, 0)).toBe(true);
    expect(isPhoneScreen(Number.NaN, 800)).toBe(true);
  });

  it("shows one pane at a time on phones and compact viewports, and lets tablets split", () => {
    expect(singlePaneFor("mobile", false, true)).toBe(true);
    expect(singlePaneFor("mobile", false, false)).toBe(false);
    // An iPad in Split View narrow enough to be compact.
    expect(singlePaneFor("mobile", true, false)).toBe(true);
    expect(singlePaneFor("desktop", false, true)).toBe(false);
    expect(singlePaneFor("compact", true, false)).toBe(true);
  });
});
