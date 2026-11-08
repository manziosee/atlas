import type { Config, Context } from "@netlify/functions";
import { handleOverview } from "../ops/core";
import { deps, opsRequest } from "../ops/netlify";

export default (req: Request, context: Context) => handleOverview(opsRequest(req, context), deps());

export const config: Config = { path: "/ops-api/overview", method: "GET", rateLimit: { windowLimit: 60, windowSize: 60, aggregateBy: ["ip", "domain"] } };
