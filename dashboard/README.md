# Atenea dashboard

The dashboard is a React Router v7 SPA compiled by Bun and embedded by the Go
binary. Production never runs a Node or Bun server.

```sh
bun install --frozen-lockfile
bun run dev
```

The development server proxies `/api` to `http://127.0.0.1:8788`. Set
`ATENEA_DASHBOARD_API` to use an isolated fixture or another Atenea instance.

```sh
bun run check
bun run build
```

`build` writes the client to `internal/dashboard/web/dist/`. Keep that
directory in sync so a plain `go build ./...` remains self-contained.
