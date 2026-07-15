# Documentation site

The vee documentation site lives under `site/` and is a [Hugo](https://gohugo.io/)
project using the [hugo-geekdoc](https://github.com/thegeeklab/hugo-geekdoc) theme.
It is published to Cloudflare Workers (static assets) at <https://vee.benehiko.com/>.

## Layout

```
site/
├── hugo.toml        # site config (baseURL, theme, params)
├── wrangler.toml    # Cloudflare Workers config (assets = ./public)
├── content/         # Markdown pages
├── layouts/         # template overrides
├── static/          # static assets
├── themes/          # hugo-geekdoc (pulled as a Hugo/Go module)
└── public/          # build output (generated; deployed to Cloudflare)
```

The theme is consumed as a Hugo Module, so the build needs Go on `PATH` to
resolve `go.mod` in the repo root.

## Local preview

```sh
cd site
hugo server           # live-reload dev server at http://localhost:1313/
hugo --minify         # produce the static site under site/public/
```

Hugo must be the **extended** edition (the theme uses SCSS).

## Deployment

Deployment is automated by
[`.github/workflows/docs.yml`](../.github/workflows/docs.yml). On a push to `main`
that touches `site/**` (or the workflow itself), CI:

1. Checks out the repo and sets up Go (for Hugo modules) and Hugo extended.
2. Runs `hugo --minify` in `site/` to build the static site into `site/public/`.
3. Deploys the site to the Cloudflare Worker **`vee`** via
   [`cloudflare/wrangler-action`](https://github.com/cloudflare/wrangler-action)
   (`wrangler deploy`), which uploads `site/public/` as the Worker's static
   assets (see the `[assets]` binding in `site/wrangler.toml`).

The workflow can also be run manually with **workflow_dispatch**.

### Required GitHub secrets

| Secret | Purpose |
|--------|---------|
| `CLOUDFLARE_API_TOKEN` | API token scoped to **Workers Scripts: Edit** |
| `CLOUDFLARE_ACCOUNT_ID` | Cloudflare account ID that owns the Worker |

### Cloudflare setup (one-time)

- The first `wrangler deploy` creates the Worker `vee` automatically; no manual
  project creation is required.
- The custom domain `vee.benehiko.com` is attached to the Worker under
  **Workers & Pages → vee → Settings → Domains & Routes** (add a custom domain).
- `baseURL` in `site/hugo.toml` must match the deployed domain
  (`https://vee.benehiko.com/`).
