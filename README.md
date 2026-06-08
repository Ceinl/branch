# Branch

Branch is a self-hosted Markdown document editor in Go. It serves a local folder and gives you a Microsoft Word / Google Docs style editor with live Markdown rendering.

## Run

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

Use another port:

```
branch --addr 127.0.0.1:9090 .
```

## Features

- Serves only the directory you launch, such as `branch .`.

- Lists folders and files from that root.

- Opens UTF-8 text files and highlights Markdown files.

- Creates Markdown files and folders.

- Autosaves edits with `Cmd+S` / `Ctrl+S` support.

- Renders Markdown live with headings, lists, tasks, quotes, code blocks, links, and inline formatting.

- Protects APIs with a per-server token printed at startup.

-

## Notes

This is an MVP, not a full Notion replacement yet. The first version intentionally avoids external services and frontend dependencies.
