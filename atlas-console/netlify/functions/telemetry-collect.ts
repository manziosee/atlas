import type { Config, Context } from "@netlify/functions";
import { handleCollect } from "../ops/core";
import { deps, opsRequest } from "../ops/netlify";

export default (req: Request, context: Context) => handleCollect(opsRequest(req, context), deps());

export const config: Config = { path: "/telemetry/collect", method: "POST", rateLimit: { windowLimit: 120, windowSize: 60, aggregateBy: ["ip", "domain"] } };
