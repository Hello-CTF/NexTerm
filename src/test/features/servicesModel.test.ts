import { describe, expect, it } from "vitest";
import {
  filterServiceUnits,
  formatListenAddress,
  listenerTargetHost,
  parseLocalListenPort,
} from "../../features/services/model";
import type { ServiceUnit } from "../../ipc/servicesApi";

const units: ServiceUnit[] = [
  { name: "nginx.service", load: "loaded", active: "active", sub: "running", description: "HTTP server" },
  { name: "postgres.service", load: "loaded", active: "failed", sub: "failed", description: "Database" },
  { name: "cron.service", load: "loaded", active: "inactive", sub: "dead", description: "Scheduler" },
  { name: "reload.service", load: "loaded", active: "active", sub: "reloading", description: "Reloading" },
];

describe("service unit filtering", () => {
  it("filters by unit text and maps running, failed, inactive and other states", () => {
    expect(filterServiceUnits(units, "http", "all").map((unit) => unit.name)).toEqual(["nginx.service"]);
    expect(filterServiceUnits(units, "", "running").map((unit) => unit.name)).toEqual(["nginx.service"]);
    expect(filterServiceUnits(units, "", "failed").map((unit) => unit.name)).toEqual(["postgres.service"]);
    expect(filterServiceUnits(units, "", "inactive").map((unit) => unit.name)).toEqual(["cron.service"]);
    expect(filterServiceUnits(units, "", "other").map((unit) => unit.name)).toEqual(["reload.service"]);
  });

  it("treats not-found load as failed and ignores case in descriptions", () => {
    const missing = { ...units[0], name: "missing.service", load: "not-found", active: "inactive", sub: "dead" };
    expect(filterServiceUnits([missing], "", "failed")).toHaveLength(1);
    expect(filterServiceUnits(units, "DATABASE", "all").map((unit) => unit.name)).toEqual(["postgres.service"]);
  });
});

describe("finder forward target mapping", () => {
  it("uses loopback for wildcard listeners and preserves specific or IPv6 addresses", () => {
    expect(listenerTargetHost("0.0.0.0")).toBe("127.0.0.1");
    expect(listenerTargetHost("*")).toBe("127.0.0.1");
    expect(listenerTargetHost("::")).toBe("127.0.0.1");
    expect(listenerTargetHost("10.0.0.8")).toBe("10.0.0.8");
    expect(listenerTargetHost("2001:db8::1")).toBe("2001:db8::1");
    expect(formatListenAddress("::", 443)).toBe("[::]:443");
  });

  it("accepts only integer local ports in range", () => {
    expect(parseLocalListenPort("18080")).toBe(18080);
    for (const value of ["0", "65536", "80;id", "1.5", ""]) {
      expect(parseLocalListenPort(value)).toBeNull();
    }
  });
});
