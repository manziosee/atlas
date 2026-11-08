import type { Config, Context } from "@netlify/functions";
import { handleLogout } from "../ops/core";
import { deps, opsRequest } from "../ops/netlify";

export default (req: Request, context: Context) => handleLogout(opsRequest(req, context), deps());

export const config: Config = { path: "/ops-api/logout", method: "POST" };
