import type { Config, Context } from "@netlify/functions";
import { handlePassword } from "../ops/core";
import { deps, opsRequest } from "../ops/netlify";

export default (req: Request, context: Context) => handlePassword(opsRequest(req, context), deps());

export const config: Config = { path: "/ops-api/password", method: "POST", rateLimit: { windowLimit: 10, windowSize: 60, aggregateBy: ["ip", "domain"] } };
