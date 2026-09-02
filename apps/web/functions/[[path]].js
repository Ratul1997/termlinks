const RELAY_ORIGIN = "https://termlinks-relay.ratulbhowmick66.workers.dev";

export async function onRequest(context) {
  const incoming = new URL(context.request.url);
  if (!incoming.pathname.startsWith("/ws/")) {
    return context.next();
  }

  const target = new URL(`${incoming.pathname}${incoming.search}`, RELAY_ORIGIN);
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
