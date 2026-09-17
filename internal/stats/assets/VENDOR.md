# Vendored third-party assets

Pinned copies, committed deliberately so a build never depends on a CDN.
Mirror any change into `internal/stats/assets/` by hand — that directory is
what `//go:embed assets` actually serves, and nothing checks the two are
identical.

| file | version | source |
|------|---------|--------|
| `chart.umd.min.js` | 4.4.1 | https://cdnjs.cloudflare.com/ajax/libs/Chart.js/4.4.1/chart.umd.min.js |
| `htmx.min.js` | 2.0.10 | https://cdnjs.cloudflare.com/ajax/libs/htmx/2.0.10/htmx.min.js |
