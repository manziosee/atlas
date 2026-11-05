import { describe, expect, it } from "vitest";
import { buildSpec, type FormValues } from "@/components/console/service-form";

const base: FormValues = { id: "", name: "payments", address: "10.0.0.8:8080", version: "", tags: "a, b", mode: "ttl", ttl: "15", check: "", interval: "", draining: false };

describe("buildSpec", () => {
  it("builds a TTL registration", () => {
    expect(buildSpec(base).spec).toEqual({ name: "payments", address: "10.0.0.8:8080", version: undefined, tags: ["a", "b"], draining: false, ttl_seconds: 15 });
  });

  it("builds a health-check registration with an interval", () => {
    const { spec } = buildSpec({ ...base, mode: "check", check: "http://10.0.0.8:8080/healthz", interval: "10", id: "p-1" });
    expect(spec).toMatchObject({ id: "p-1", health_check: "http://10.0.0.8:8080/healthz", check_interval_seconds: 10 });
    expect(spec?.ttl_seconds).toBeUndefined();
  });

  it("accepts IPv6 addresses and explains bad input", () => {
    expect(buildSpec({ ...base, address: "[fd00::1]:8080" }).spec).toBeDefined();
    const { spec, errors } = buildSpec({ ...base, name: "-bad", address: "nohost", ttl: "0", mode: "ttl" });
    expect(spec).toBeUndefined();
    expect(Object.keys(errors).sort()).toEqual(["address", "name", "ttl"]);
    expect(buildSpec({ ...base, mode: "check", check: "ftp://x" }).errors.check).toBeDefined();
  });
});
