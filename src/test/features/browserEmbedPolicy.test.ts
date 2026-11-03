import { describe, expect, it } from "vitest";
import { planEmbed, sandboxFor } from "../../features/browser/embedPolicy";

const desktop = { appOrigin: "wails://wails", web: false, mobile: false };
const windows = { appOrigin: "http://wails.localhost", web: false, mobile: false };
const web = { appOrigin: "https://nexterm.example.com", web: true, mobile: false };

describe("planEmbed", () => {
  it("embeds https pages on desktop and in the browser", () => {
    expect(planEmbed("https://example.com/docs", desktop)).toEqual({ kind: "embed", url: "https://example.com/docs" });
    expect(planEmbed("https://example.com/docs", web)).toEqual({ kind: "embed", url: "https://example.com/docs" });
  });

  it("embeds http pages on desktop only", () => {
    expect(planEmbed("http://localhost:3000/", desktop).kind).toBe("embed");
    expect(planEmbed("http://localhost:3000/", web).kind).toBe("external");
  });

  it("refuses the application's own origin", () => {
    expect(planEmbed("http://wails.localhost/index.html", windows).kind).toBe("blocked");
    expect(planEmbed("https://nexterm.example.com/anything", web).kind).toBe("blocked");
  });

  it("allows the same host on another port", () => {
    expect(planEmbed("https://nexterm.example.com:8443/", web).kind).toBe("embed");
  });

  it("refuses Wails asset hosts in any scheme, port and case", () => {
    for (const url of ["HTTP://WAILS.LOCALHOST:80/", "https://wails.localhost:9999/x", "http://wails/", "https://Wails.Localhost/", "wails://wails/"]) {
      expect(planEmbed(url, desktop).kind, url).toBe("blocked");
    }
  });

  it("refuses non-web schemes", () => {
    for (const url of ["javascript:alert(1)", "data:text/html,<b>x</b>", "file:///etc/passwd", "mailto:a@b.c", "ssh://host", "not a url"]) {
      expect(planEmbed(url, desktop).kind, url).toBe("blocked");
    }
  });

  it("sends mobile devices to the system browser", () => {
    expect(planEmbed("https://example.com/", { ...desktop, mobile: true }).kind).toBe("external");
    expect(planEmbed("https://example.com/", { ...web, mobile: true }).kind).toBe("external");
  });

  it("still blocks Wails hosts on mobile", () => {
    expect(planEmbed("http://wails.localhost/", { ...desktop, mobile: true }).kind).toBe("blocked");
  });
});

describe("sandboxFor", () => {
  it("grants scripts, forms and same-origin to foreign pages", () => {
    expect(sandboxFor("https://example.com/", "https://nexterm.example.com").split(" ").sort()).toEqual([
      "allow-forms",
      "allow-same-origin",
      "allow-scripts",
    ]);
  });

  it("drops allow-same-origin for the application's own origin", () => {
    const tokens = sandboxFor("https://nexterm.example.com/x", "https://nexterm.example.com").split(" ");
    expect(tokens).not.toContain("allow-same-origin");
    expect(tokens).toContain("allow-scripts");
  });

  it("never adds popups, top navigation or modals", () => {
    const value = sandboxFor("https://example.com/", "wails://wails");
    for (const token of ["allow-popups", "allow-top-navigation", "allow-modals"]) expect(value).not.toContain(token);
  });
});
