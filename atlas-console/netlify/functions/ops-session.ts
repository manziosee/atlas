import type { Config, Context } from "@netlify/functions";
import { handleSession } from "../ops/core";
import { deps, opsRequest } from "../ops/netlify";

export default (req: Request, context: Context) => handleSession(opsRequest(req, context), deps());

export const config: Config = { path: "/ops-api/session", method: "GET" };
