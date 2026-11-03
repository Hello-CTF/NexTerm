/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  SESSION_SIDEBAR_STORAGE_KEY,
  getSessionSidebarOpen,
  setSessionSidebarOpen,
  subscribeSessionSidebar,
} from "../app/preferences";

beforeEach(() => localStorage.clear());
afterEach(() => vi.restoreAllMocks());

describe("session sidebar preference", () => {
  it("defaults to closed", () => {
    expect(getSessionSidebarOpen()).toBe(false);
  });

  it("persists the open state locally", () => {
    setSessionSidebarOpen(true);
    expect(getSessionSidebarOpen()).toBe(true);
    expect(localStorage.getItem(SESSION_SIDEBAR_STORAGE_KEY)).not.toBeNull();
    setSessionSidebarOpen(false);
    expect(getSessionSidebarOpen()).toBe(false);
  });

  it("reads an invalid stored value as closed", () => {
    localStorage.setItem(SESSION_SIDEBAR_STORAGE_KEY, "garbage");
    expect(getSessionSidebarOpen()).toBe(false);
  });

  it("does not throw when storage is unavailable", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("denied");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("denied");
    });
    expect(getSessionSidebarOpen()).toBe(false);
    expect(() => setSessionSidebarOpen(true)).not.toThrow();
    setSessionSidebarOpen(false);
  });

  it("notifies subscribers until they unsubscribe", () => {
    const listener = vi.fn();
    const off = subscribeSessionSidebar(listener);
    setSessionSidebarOpen(true);
    expect(listener).toHaveBeenCalledTimes(1);
    off();
    setSessionSidebarOpen(false);
    expect(listener).toHaveBeenCalledTimes(1);
  });

  it("keeps toggling and notifying when only writes fail", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("denied");
    });
    const listener = vi.fn();
    const off = subscribeSessionSidebar(listener);
    setSessionSidebarOpen(true);
    expect(getSessionSidebarOpen()).toBe(true);
    setSessionSidebarOpen(false);
    expect(getSessionSidebarOpen()).toBe(false);
    expect(listener).toHaveBeenCalledTimes(2);
    off();
  });
});
