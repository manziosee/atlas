# Atlas dashboard

The node serves the embedded dashboard at `/dashboard/`. It displays the
node's membership view, replicated service instances, summary counts, and live
membership events. Enter an API bearer token in the page to authorize requests;
the token is held in page memory and is not stored in the browser. API data
remains protected by the node's normal bearer-token middleware.

Sources are in `internal/api/dashboard/` and are embedded into the Go binary.
The page uses same-origin HTTP requests and a streaming fetch for SSE so it can
send the bearer token without putting it in a URL.
