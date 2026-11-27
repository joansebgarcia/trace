# Solid 2 for the embedded UI

Supersedes [ADR 0002](./0002-vue-for-embedded-ui.md). The embedded web UI is built with Solid 2 (`solid-js`/`@solidjs/web` 2.0.4 at adoption, Vite with `@solidjs/vite-plugin`, `@solidjs/router` 2, `@solid-primitives/i18n` 3) instead of Vue 3. ADR 0002 rejected Solid because its tooling still lived in `next`; since 2026-11-17 core, Vite plugin, router and i18n are stable, and the team chose to adopt a days-old major to explore what Solid 2 enables in Trace, accepting a younger ecosystem in exchange for fine-grained reactivity and a smaller bundle (~27 KB gzip with router and i18n, against ~47 KB for Vue with `vue-i18n`).

## Consequences

- Solid 2 holds writes that touch an async computation until it settles, so live Realtime state lives in its own store, filled by plain async REST loads and Signal upserts, never derived from `createMemo(async)`.
- Client routing uses `browserHistory`; the Go server returns `index.html` for any GET that is not `/api/*` or a static asset.
- Two gaps are accepted: component tests use `@solidjs/testing-library` 1.0.0-rc.2 (still a release candidate), and lint adds `eslint-plugin-solid/configs/v2` by hand because the `solid` option of `@antfu/eslint-config` targets Solid 1.
- Most third-party material and libraries still target Solid 1.x; any added dependency must have a Solid 2 release.
