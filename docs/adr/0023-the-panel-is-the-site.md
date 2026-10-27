# ADR 0023 — The panel is the site, and it is built on React

Date: 2026-10-27

## Status

Accepted. Supersedes ADR 0007 (Tabler) and the `/admin` path of ADR 0004.

## Context

Two decisions, both forced by the same thing: the panel was not pleasant to
use, and it lived behind a path that nobody remembered.

**The path.** The panel was at `/admin/` and its API at `/admin/api/`. The
service already had a public subdomain (`xellter.example.com`) that served
the API and nothing else. The owner wanted the panel on that subdomain, with
no path to type.

**The build.** The panel was plain JavaScript on a vendored copy of Tabler, a
Bootstrap admin kit. The project rule was "no build step, no CDN". The rule
was there to keep the deployment to one file and to keep the browser from
fetching anything from a third party. Both are still worth having, but a
vendored CSS file is not the same thing as no build step, and the old panel
had grown a hand-written DOM layer that was hard to change.

The design work followed the
[ui-ux-pro-max-skill](https://github.com/nextlevelbuilder/ui-ux-pro-max-skill)
reference: a three-layer token system (primitive, semantic, component), one
accent colour, hairline borders instead of shadows, and a clean neutral
console look.

## Decision

**The panel is served at the root of its host, its API at `/api`, and it is
built with React, Vite, Tailwind and shadcn/ui.**

- The panel answers every path that is not the API. The API is `/api/*`, the
  proxy is `/v1/*`, and `/healthz` is the proxy's. A deep link such as
  `/clients/demo` loads the panel shell and the page works out the rest.
- The session cookie covers `/`, not one path.
- The panel is built by Vite into static files that the Go binary embeds. The
  shipped artefact is still one file, and the browser still loads nothing from
  a CDN. The build step is a repository requirement, not a deployment one:
  `npm run build` writes `internal/admin/ui/dist`, and `go build` embeds it.
- The design system is a token file (`web/src/styles/tokens.css`) in the three
  layers the reference describes. The components follow the shadcn/ui shapes
  and are in `web/src/components/ui`.
- Light and dark are the same tokens with the semantic layer swapped, chosen
  by `data-theme` on the root element.

## Consequences

- `internal/admin/ui/vendor/tabler.min.css` and the hand-written
  `app.js` are gone. The panel is React under `web/src`.
- The repository grows a `web/` directory with a `package.json` and a
  `node_modules` (git-ignored). `scripts/ci.sh` builds the panel before the
  Go tests, so a panel that does not compile fails the build.
- The panel is a single-page app: the client-side router owns the paths, and
  the server answers any unknown path with the shell.
- `internal/admin` embeds `ui/dist`, so the Go build fails if the panel was
  never built. The CI script builds it first.
