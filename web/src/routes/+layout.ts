// SPA served by the Go embed: no SSR, no per-route prerender. adapter-static's
// index.html fallback lets the client router resolve deep links.
export const ssr = false;
export const prerender = false;
