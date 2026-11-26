---
status: superseded by ADR-0003
---

# Vue 3 with vue-i18n for the embedded UI

The embedded web UI of Amber (served from the Go binary via `go:embed`) is built with Vue 3 and `vue-i18n`, choosing ecosystem maturity over bundle size. A prototype of the same screen (Issue list with live updates over the Realtime stream, es/en texts) in Svelte 5, Solid 2, Preact 11 and Vue 3.5 showed the four are equivalent on SSE integration and component code size, and differ mainly in bundle size (gzip: Preact 10.6 KB, Solid 17 KB, Svelte 20.5 KB, Vue 27.5 KB; Vue with `vue-i18n` 47 KB) and ecosystem. Vue has the largest application-level ecosystem of the four and the only mature, maintained i18n library among them (`vue-i18n`; `svelte-i18n` and `preact-i18n` are stale, Solid 2 tooling is still in pre-release). Bundle size was not decisive: the UI is an admin surface, cached after first load, inside a binary of tens of MB, and no size budget exists.

## Consequences

- The UI ships the heaviest bundle of the candidates (~40–47 KB gzip baseline); if a size budget is ever set, this ADR is the one to revisit.
- i18n uses `vue-i18n` and its message format; precompiling messages at build time (to drop the runtime message compiler) is left to the i18n strategy.
- `@antfu/eslint-config` runs with its Vue option; `web/` tooling is Vite + `@vitejs/plugin-vue`.
- Prototype: branch `prototype/ui-framework` (`web/prototype-framework/`), never merged.
