// The service worker that lets Chrome and Samsung Internet install the dashboard as an app.
// It stores nothing: every request goes to the server as usual, since the pages hold the user's
// own data. A page opened without a connection gets a short offline notice instead of the
// browser's error page.

const offlinePage = `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1"><title>Offline · Daily Dashboard</title>
<style>body{margin:0;min-height:100vh;display:grid;place-items:center;font:15px/1.5 system-ui,sans-serif;
background:#f6f5f2;color:#1d1d1b;text-align:center;padding:16px}
@media (prefers-color-scheme:dark){body{background:#151614;color:#ecebe6}}</style></head>
<body><div><h1 style="font-size:20px">You're offline</h1><p>The dashboard needs a connection. Try again once you're back online.</p></div></body></html>`;

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (event) => event.waitUntil(self.clients.claim()));

self.addEventListener("fetch", (event) => {
  if (event.request.mode !== "navigate") return;
  event.respondWith(
    fetch(event.request).catch(
      () => new Response(offlinePage, { status: 503, headers: { "Content-Type": "text/html; charset=utf-8" } }),
    ),
  );
});
