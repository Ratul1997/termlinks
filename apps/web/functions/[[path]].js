export async function onRequest(context) {
  const incoming = new URL(context.request.url);
  if (!incoming.pathname.startsWith("/ws/")) {
    return context.next();
  }

  const relayOrigin = validRelayOrigin(context.env.RELAY_ORIGIN);
  if (!relayOrigin) {
    return Response.json(
      { error: "RELAY_ORIGIN is not configured for this Pages project" },
      { status: 503, headers: { "Cache-Control": "no-store" } },
    );
  }
  const target = new URL(`${incoming.pathname}${incoming.search}`, relayOrigin);
  const headers = new Headers(context.request.headers);
  headers.delete("Host");
  const request = new Request(target, {
    method: context.request.method,
    headers,
    body: context.request.body,
    redirect: "manual",
  });
  return fetch(request);
}

function validRelayOrigin(value) {
  if (typeof value !== "string") return undefined;
  try {
    const url = new URL(value);
    const localDevelopment = url.protocol === "http:" && (url.hostname === "127.0.0.1" || url.hostname === "localhost");
    if (url.protocol !== "https:" && !localDevelopment) return undefined;
    if (url.username || url.password || url.pathname !== "/" || url.search || url.hash) return undefined;
    return url;
  } catch {
    return undefined;
  }
}
