import { describe, expect, it } from "vitest";
import { webhookUrlProblem } from "@/components/console/webhooks-panel";

describe("webhookUrlProblem", () => {
  it("accepts http and https endpoints, including private hosts", () => {
    expect(webhookUrlProblem("https://hooks.example.com/atlas")).toBeNull();
    expect(webhookUrlProblem("http://10.0.0.5:8080/hook?token=x")).toBeNull();
  });

  it("rejects what the node would refuse", () => {
    expect(webhookUrlProblem("not a url")).not.toBeNull();
    expect(webhookUrlProblem("ftp://example.com/")).not.toBeNull();
    expect(webhookUrlProblem("https://user:pw@example.com/")).not.toBeNull();
    expect(webhookUrlProblem("https://example.com/#x")).not.toBeNull();
    expect(webhookUrlProblem("http://169.254.169.254/latest/meta-data/")).not.toBeNull();
    expect(webhookUrlProblem("http://[fe80::1]/hook")).not.toBeNull();
  });
});
