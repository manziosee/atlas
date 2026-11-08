import type { Config, Context } from "@netlify/functions";
import { handleLogin } from "../ops/core";
import { deps, opsRequest } from "../ops/netlify";

export default (req: Request, context: Context) => handleLogin(opsRequest(req, context), deps());

export const config: Config = { path: "/ops-api/login", method: "POST", rateLimit: { windowLimit: 10, windowSize: 60, aggregateBy: ["ip", "domain"] } };
