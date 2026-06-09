# Branch

Branch is a self-hosted Markdown document editor written in Go. It serves a local folder in the browser and gives you a Google Docs-style editor with live Markdown rendering and lightweight collaboration

## Run

Run against the current directory:

```
go run . .
```

Build a `branch` binary:

```
go build -o branch .
./branch .
```

After install, the command shape is:

```
branch .
```

Use another port, or `:0` for a random free port:

```
branch --addr 127.0.0.1:9090 .
branch --open --addr :0 .
```

`--open` launches your browser once the server is listening. `branch version` prints the version.

## Share

Local mode skips authentication and is meant for your own machine:

```
branch .
```

Shared mode requires a public HTTPS origin and authenticates users with Shoo / Google sign-in:

```
branch share https://docs.example.com .
```

This is equivalent to listening on `0.0.0.0:8080` and using that URL as the Shoo origin:

```
branch --addr 0.0.0.0:8080 --origin https://docs.example.com .
```

`0.0.0.0` is only a listen address, not a browser URL. For local development with Shoo, open `http://localhost:8080`. For other machines, use an HTTPS public origin and pass it with `--origin` or `branch share`.

With Cloudflare Tunnel:

```
cloudflared tunnel --url http://localhost:8080
branch share https://your-tunnel.trycloudflare.com .
```

## Features

- Serves only the directory you launch, such as `branch .`.

- Lists folders and files from that root.

- Opens UTF-8 text files and highlights Markdown files.

- Creates, renames, and deletes Markdown files and folders. Version history follows renames.

- Autosaves edits with `Cmd+S` / `Ctrl+S` support.

- Renders Markdown live with headings, lists, tasks, quotes, code blocks, links, and inline formatting.

- Authenticates shared users with Shoo / Google sign-in and verifies Shoo ID tokens server-side.

- Shows live drafts, saved changes, collaborator presence, and colored remote cursors for users viewing the same file.

- Records every save of a Markdown file as a git commit and shows the version history as a tree you can browse, diff, name, and restore from.

- Follows your system theme with a full dark mode.

## Save history

Every save of a Markdown file is recorded as a real git commit in a hidden bare repository at `.branch/history.git` inside the served folder. Your own files are never touched by git; the history repo only stores snapshots.

Open a file and press the History button (or `Cmd+Shift+H`) to see the version tree. Click a node to see what changed in that save (the Changes tab) or the full document at that point (the Document tab), give important versions a name, and Restore to bring one back. Restoring does not delete anything: the next save branches off the restored version, so the history forms a tree rather than a flat list, and every path you ever took is still reachable.

If a file changed on the server after you loaded it (for example another collaborator saved while your live connection was down), Branch refuses the save and asks before overwriting; the other version stays reachable in history either way. Deleting a file keeps its history, so recreating it at the same path continues the old tree.

Rapid autosaves from the same client within two minutes coalesce into a single version, so the tree stays readable. Saving identical content creates no new version.

History requires `git` on the server's PATH. Disable it with `--no-history`. The `.branch` folder is hidden from the file list and cannot be opened or edited through the app.

## Auth

Every signed-in user has full read and write access to the served folder. To restrict who can sign in, pass an email allowlist:

```
branch share --allow alice@example.com,bob@example.com https://docs.example.com .
```

Shoo identity is kept in browser storage, and Branch also sets an HttpOnly session cookie. In shared mode you should not need to re-login on page reload. You may need to sign in again after token expiry, browser storage or cookie cleanup, server restart, or if the public origin changes.

## Collaboration

Multiple Shoo-authenticated users can open and edit the same file. Branch sends live drafts, save events, cursor positions, joins, and disconnects to everyone viewing that file.

Branch prefers Server-Sent Events for live updates. If a proxy or tunnel buffers the event stream, such as some `trycloudflare.com` sessions, the browser falls back to a short JSON polling loop so collaborators still receive changes.

Remote live edits are applied block by block while protecting the block you are actively editing. If another user saves changes in a block you also edited locally, Branch keeps your local edits and warns before overwriting the remote version.

## Notes

This is an MVP, not a full Notion replacement yet. Collaboration is block-level live sync, not a full CRDT engine.
