# Branch

Branch is a self-hosted Markdown document editor written in Go. It serves a local folder in the browser and gives you a Google Docs-style editor with live Markdown rendering and lightweight collaboration.

## Run

Run against the current directory:

```sh
go run . .
```

Build a `branch` binary:

```sh
go build -o branch .
./branch .
```

After install, the command shape is:

```sh
branch .
```

Use another port:

```sh
branch --addr 127.0.0.1:9090 .
```

## Share

Local mode skips authentication and is meant for your own machine:

```sh
branch .
```

Shared mode requires a public HTTPS origin and authenticates users with Shoo / Google sign-in:

```sh
branch share https://docs.example.com .
```

This is equivalent to listening on `0.0.0.0:8080` and using that URL as the Shoo origin:

```sh
branch --addr 0.0.0.0:8080 --origin https://docs.example.com .
```

`0.0.0.0` is only a listen address, not a browser URL. For local development with Shoo, open `http://localhost:8080`. For other machines, use an HTTPS public origin and pass it with `--origin` or `branch share`.

With Cloudflare Tunnel:

```sh
cloudflared tunnel --url http://localhost:8080
branch share https://your-tunnel.trycloudflare.com .
```

## Features

- Serves only the directory you launch, such as `branch .`.
- Lists folders and files from that root.
- Opens UTF-8 text files and highlights Markdown files.
- Creates Markdown files and folders.
- Autosaves edits with `Cmd+S` / `Ctrl+S` support.
- Renders Markdown live with headings, lists, tasks, quotes, code blocks, links, and inline formatting.
- Authenticates shared users with Shoo / Google sign-in and verifies Shoo ID tokens server-side.
- Shows live drafts, saved changes, collaborator presence, and colored remote cursors for users viewing the same file.

## Auth

Shoo identity is kept in browser storage, and Branch also sets an HttpOnly session cookie. In shared mode you should not need to re-login on page reload. You may need to sign in again after token expiry, browser storage or cookie cleanup, server restart, or if the public origin changes.

## Collaboration

Multiple Shoo-authenticated users can open and edit the same file. Branch sends live drafts, save events, cursor positions, joins, and disconnects to everyone viewing that file.

Branch prefers Server-Sent Events for live updates. If a proxy or tunnel buffers the event stream, such as some `trycloudflare.com` sessions, the browser falls back to a short JSON polling loop so collaborators still receive changes.

Remote live edits are applied block by block while protecting the block you are actively editing. If another user saves changes in a block you also edited locally, Branch keeps your local edits and warns before overwriting the remote version.

## Notes

This is an MVP, not a full Notion replacement yet. Collaboration is block-level live sync, not a full CRDT engine.
