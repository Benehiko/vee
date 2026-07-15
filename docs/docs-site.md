# Documentation site

The vee documentation site lives under `site/` and is a [Hugo](https://gohugo.io/)
project using the [hugo-geekdoc](https://github.com/thegeeklab/hugo-geekdoc) theme.
It is published to Cloudflare Pages at <https://vee.benehiko.com/>.

## Layout

```
site/
├── hugo.toml        # site config (baseURL, theme, params)
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
3. Deploys `site/public/` to the Cloudflare Pages project **`vee`** via
   [`cloudflare/wrangler-action`](https://github.com/cloudflare/wrangler-action)
   (`wrangler pages deploy`).

The workflow can also be run manually with **workflow_dispatch**.

### Required GitHub secrets

| Secret | Purpose |
|--------|---------|
| `CLOUDFLARE_API_TOKEN` | API token scoped to **Cloudflare Pages: Edit** |
| `CLOUDFLARE_ACCOUNT_ID` | Cloudflare account ID that owns the Pages project |

### Cloudflare setup (one-time)

- A Cloudflare Pages project named `vee` must exist (create an empty one via the
  dashboard or `wrangler pages project create vee`).
- The custom domain `vee.benehiko.com` is attached to that project under
  **Pages → vee → Custom domains**.
- `baseURL` in `site/hugo.toml` must match the deployed domain
  (`https://vee.benehiko.com/`).
