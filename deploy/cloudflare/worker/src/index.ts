// The Worker in front of https://nitrovin.app: every request goes to one
// warm container running the released image's `nitrovin serve`, and the
// GETs whose answer is a function of the URL and the release are served
// from the Cache API. It never reads or logs a VIN.
import { Container } from "@cloudflare/containers";

/** The container: the released image, serving the API, the form and /mcp. */
export class Nitrovin extends Container<Env> {
	defaultPort = 8080;
	// serve opens its listener only once the data has loaded, so any answer
	// from /healthz means ready. The host part is the Host header sent.
	pingEndpoint = "localhost/healthz";
	// Longer than the keep-warm cron's period, so the container never idles
	// out; every request renews it too.
	sleepAfter = "10m";
	// A decoder never calls out.
	enableInternet = false;
	// The command (serve with --cors) is the Dockerfile's CMD: an
	// entrypoint set here replaces the image's command, not its entrypoint.
}

// The service is stateless, so one name: one Durable Object, one container.
// The object stays where it is first created, and the container's every
// request goes through it, so its place is chosen: Eastern North America,
// beside the container and the callers (#126). A hint counts only when the
// object is created, so a new place needs a new name. The object before this
// one, "nitrovin", ran in Amsterdam; nothing calls it now, and its container
// sleeps after sleepAfter.
const INSTANCE = "nitrovin-enam";
const LOCATION: DurableObjectLocationHint = "enam";

// How long a cacheable answer is kept, and may be kept by clients. The key
// names the release that computed it, so a new one never meets it.
const CACHE_CONTROL = "public, max-age=86400";

export default {
	async fetch(request, env): Promise<Response> {
		const url = new URL(request.url);
		const container = nitrovin(env);
		if (request.method !== "GET" || !cacheable(url)) {
			const response = await container.fetch(forward(request, url));
			if (request.method !== "GET" && request.method !== "HEAD") {
				return response;
			}
			return withHeaders(response, { "Cache-Control": "no-store" });
		}

		// A hit never reaches the container: the key's release is the one
		// this deploy pinned (RELEASE), not one read from it.
		const key = env.RELEASE ? cacheKey(url, env.RELEASE) : undefined;
		if (key) {
			const hit = await caches.default.match(key);
			if (hit) {
				return withHeaders(hit, { "Cf-Cache-Status": "HIT" });
			}
		}
		const response = await container.fetch(forward(request, url));
		// Store a 200 only from the release the key names: during a rollout
		// the container can still be the previous one.
		if (response.status !== 200 || !key || !(await serves(container, env.RELEASE))) {
			return withHeaders(response, { "Cache-Control": "no-store", "Cf-Cache-Status": "BYPASS" });
		}
		const out = withHeaders(response, { "Cache-Control": CACHE_CONTROL, "Cf-Cache-Status": "MISS" });
		await caches.default.put(key, out.clone());
		return out;
	},

	// Keep warm: starts the container if it stopped, renews its sleepAfter.
	async scheduled(_controller, env): Promise<void> {
		const response = await nitrovin(env).fetch("http://localhost/healthz");
		if (!response.ok) {
			throw new Error(`keep-warm: GET /healthz answered ${response.status}`);
		}
	},
} satisfies ExportedHandler<Env>;

/** The one Durable Object, created in LOCATION if this call creates it. */
function nitrovin(env: Env): DurableObjectStub<Nitrovin> {
	return env.NITROVIN.get(env.NITROVIN.idFromName(INSTANCE), { locationHint: LOCATION });
}

// The release this isolate has seen the container serve, once it was
// RELEASE. An instance serves one image for its life and a rollout only
// ends on this deploy's, while a new deploy is a new isolate: once seen, a
// miss need not ask again (#126). Until then, every miss asks.
let served: string | undefined;

/** Whether the container serves release: asked of it until it once says so. */
async function serves(container: DurableObjectStub<Nitrovin>, release: string): Promise<boolean> {
	if (served === release) {
		return true;
	}
	if ((await identity(container)) !== release) {
		return false;
	}
	served = release;
	return true;
}

/**
 * The request as the container gets it: plain HTTP, X-Forwarded-Proto
 * naming the client's scheme (the form shows the URLs the client used), the
 * client's Host, except on /mcp. The MCP SDK refuses a request that reaches
 * a loopback address under another Host (DNS-rebinding protection), and
 * which address the container sees is the platform's choice.
 */
function forward(request: Request, url: URL): Request {
	const host = url.pathname === "/mcp" ? "localhost" : url.host;
	const headers = new Headers(request.headers);
	headers.set("X-Forwarded-Proto", url.protocol.replace(/:$/, ""));
	return new Request(`http://${host}${url.pathname}${url.search}`, {
		method: request.method,
		headers,
		body: request.body,
		redirect: "manual",
	});
}

/** The GETs whose 200 is a function of the URL and the release alone. */
function cacheable(url: URL): boolean {
	return (
		url.pathname.startsWith("/v1/decode/") ||
		url.pathname === "/v1/info" ||
		(url.pathname === "/" && url.search !== "")
	);
}

/**
 * The release the container serves, as RELEASE names one: decoder version,
 * data month and artifact hash from its /v1/info; undefined when it cannot
 * say.
 */
async function identity(container: DurableObjectStub<Nitrovin>): Promise<string | undefined> {
	const response = await container.fetch("http://localhost/v1/info");
	if (!response.ok) {
		return undefined;
	}
	const info = (await response.json()) as { decoder_version: string; data_month: string; artifact_hash: string };
	return [info.decoder_version, info.data_month, info.artifact_hash].join("/");
}

/** The cache key: the release, then the path and query as sent. */
function cacheKey(url: URL, release: string): string {
	return `${url.origin}/.cache/${encodeURIComponent(release)}${url.pathname}${url.search}`;
}

function withHeaders(response: Response, headers: Record<string, string>): Response {
	const out = new Response(response.body, response);
	for (const [name, value] of Object.entries(headers)) {
		out.headers.set(name, value);
	}
	return out;
}
