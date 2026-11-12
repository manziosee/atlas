import { describe, expect, it } from "vitest";
import { buildSpec, parseMetadata, type FormValues } from "@/components/console/service-form";

const base: FormValues = { id: "", name: "payments", address: "10.0.0.8:8080", version: "", tags: "a, b", mode: "ttl", ttl: "15", check: "", interval: "", draining: false, trafficWeight: "1", metadata: "" };

describe("buildSpec", () => {
  it("builds a TTL registration", () => {
    expect(buildSpec(base).spec).toEqual({ name: "payments", address: "10.0.0.8:8080", version: undefined, tags: ["a", "b"], draining: false, traffic_weight: 1, ttl_seconds: 15 });
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
    expect(buildSpec({ ...base, trafficWeight: "1001" }).errors.trafficWeight).toBeDefined();
  });
});

describe("metadata", () => {
  it("parses key=value lines into the spec", () => {
    const { spec } = buildSpec({ ...base, metadata: "team=payments\n\n protocol = grpc \nempty=" });
    expect(spec?.metadata).toEqual({ team: "payments", protocol: "grpc", empty: "" });
    expect(buildSpec(base).spec?.metadata).toBeUndefined();
  });

  it("keeps everything after the first = in the value", () => {
    expect(parseMetadata("url=https://x.test/?a=b").metadata).toEqual({ url: "https://x.test/?a=b" });
  });

  it("rejects keys and sizes the node would reject", () => {
    expect(parseMetadata("bad key=v").error).toBeDefined();
    expect(parseMetadata(".hidden=v").error).toBeDefined();
    expect(parseMetadata("team=a\nteam=b").error).toBeDefined();
    expect(parseMetadata(Array.from({ length: 17 }, (_, i) => `k${i}=v`).join("\n")).error).toBeDefined();
    expect(parseMetadata(`a=${"v".repeat(300)}`).error).toBeDefined();
    expect(parseMetadata(["a", "b", "c"].map((k) => `${k}=${"v".repeat(200)}`).join("\n")).error).toBeDefined();
    expect(buildSpec({ ...base, metadata: "bad key=v" }).errors.metadata).toBeDefined();
  });
});
