import http from "k6/http";
import { check, sleep } from "k6";

const baseURL = __ENV.ATLAS_API_URL || "http://atlas-1:8080";
const token = __ENV.ATLAS_TOKEN;

export const options = {
  vus: Number(__ENV.K6_VUS || 10),
  duration: __ENV.K6_DURATION || "30s",
  thresholds: {
    http_req_failed: ["rate<0.01"],
    http_req_duration: ["p(95)<500"],
    checks: ["rate>0.99"],
  },
};

export default function () {
  const params = { headers: { Authorization: `Bearer ${token}` } };
  const members = http.get(`${baseURL}/v1/members`, params);
  check(members, {
    "members API returns 200": (response) => response.status === 200,
    "members API returns a member list": (response) => Array.isArray(response.json("members")),
  });

  const services = http.get(`${baseURL}/v1/services`, params);
  check(services, {
    "services API returns 200": (response) => response.status === 200,
    "services API returns a service list": (response) => Array.isArray(response.json("services")),
  });

  // Keep the default profile under Atlas's per-client API rate limit while
  // still creating concurrent read traffic from multiple virtual users.
  sleep(0.5);
}
