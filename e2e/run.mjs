// End-to-end smoke suite: builds nothing itself, expects the `branch` binary
// next to the repo root (CI builds it first). Starts a server on a random
// port against a temp workspace and drives headless Chromium through the
// core flows. Exits non-zero on the first failure.
import { spawn } from "node:child_process";
import { mkdtempSync, writeFileSync, readFileSync, rmSync } from "node:fs";
import { tmpdir, homedir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const repo = dirname(dirname(fileURLToPath(import.meta.url)));
const binary = process.env.BRANCH_BIN || join(repo, "branch");

const TOUGH = `---
title: Front matter
---

# Heading

Paragraph line one
continued on second line.

| Name | Value |
|------|-------|
| a    | 1     |

- top level
  - nested item

1. first
5. five

> quote line

\`\`\`go
func main() {}
\`\`\`

<div>raw html</div>

Final paragraph.
`;

let failures = 0;
function check(name, ok, detail = "") {
  if (ok) {
    console.log(`ok   ${name}`);
  } else {
    failures++;
    console.error(`FAIL ${name} ${detail}`);
  }
}

async function loadChromium() {
  try {
    const { chromium } = await import("playwright");
    return { chromium, options: {} };
  } catch {
    const { chromium } = await import("playwright-core");
    const fallback = join(
      homedir(),
      "Library/Caches/ms-playwright/chromium_headless_shell-1200/chrome-headless-shell-mac-arm64/chrome-headless-shell",
    );
    return { chromium, options: { executablePath: process.env.CHROMIUM_PATH || fallback } };
  }
}

const workspace = mkdtempSync(join(tmpdir(), "branch-e2e-"));
writeFileSync(join(workspace, "tough.md"), TOUGH);
writeFileSync(join(workspace, "findme.md"), "# Findme\n\nthe sourdough secret\n");

const server = spawn(binary, ["--addr", "127.0.0.1:0", workspace], { stdio: ["ignore", "pipe", "inherit"] });
const base = await new Promise((resolve, reject) => {
  let buffer = "";
  const timer = setTimeout(() => reject(new Error("server did not start")), 10000);
  server.stdout.on("data", (chunk) => {
    buffer += chunk;
    const match = buffer.match(/Open (http:\/\/[^\s]+)\//);
    if (match) {
      clearTimeout(timer);
      resolve(match[1]);
    }
  });
  server.on("exit", () => reject(new Error("server exited early")));
});

async function api(path, method = "GET", body = null) {
  const response = await fetch(base + path, {
    method,
    body: body ? JSON.stringify(body) : null,
  });
  if (!response.ok) throw new Error(`${method} ${path}: ${response.status}`);
  return response.json();
}

const { chromium, options } = await loadChromium();
const browser = await chromium.launch(options);
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
const pageErrors = [];
page.on("pageerror", (error) => pageErrors.push(String(error)));
page.on("dialog", (dialog) => dialog.accept());

try {
  // 1. Lossless round trip
  await page.goto(`${base}/#/doc/tough.md`);
  await page.waitForSelector(".block");
  check("round trip is byte-identical", await page.evaluate(() => serializeDocument() === state.content));

  // 2. Editing one block leaves the rest untouched
  await page.locator(".block-p").last().click();
  await page.keyboard.press("End");
  await page.keyboard.type(" Edited!");
  await page.waitForFunction(() => document.querySelector("#save-state").textContent === "Saved");
  const disk = readFileSync(join(workspace, "tough.md"), "utf8");
  check("edit reached disk", disk.includes("Final paragraph. Edited!"));
  check("table survived the edit", disk.includes("| Name | Value |"));
  check("nested list survived the edit", disk.includes("  - nested item"));

  // 3. History tree: a second save, then restore the first version
  await api("/api/file", "PUT", { path: "tough.md", content: TOUGH + "\nanother save\n", clientId: "e2e-2" });
  await page.click("#history-toggle");
  await page.waitForSelector(".history-row");
  const rowCount = await page.locator(".history-row").count();
  check("history has multiple versions", rowCount >= 2, `got ${rowCount}`);
  await page.locator(".history-row:not(.current)").first().click();
  await page.waitForSelector("#history-restore:not([disabled])");
  await page.click("#history-restore");
  await page.waitForFunction(() => document.querySelector("#history-restore").disabled);
  check("restore made the selected version current", true);
  const nodes = (await api("/api/file/history?path=tough.md")).nodes;
  check("restored node is current server-side", nodes.some((node) => node.current));
  await page.click("#history-close");

  // 4. Theme toggle persists across reload
  await page.click("#theme-toggle-editor");
  const dark = await page.evaluate(() => document.documentElement.dataset.theme);
  await page.reload();
  await page.waitForSelector(".block");
  const after = await page.evaluate(() => document.documentElement.dataset.theme);
  check("theme persists across reload", dark === "dark" && after === "dark");

  // 5. Workspace search
  await page.goto(`${base}/`);
  await page.waitForSelector(".docs-row");
  await page.fill("#docs-search", "sourdough");
  await page.waitForFunction(() =>
    [...document.querySelectorAll(".docs-row-meta")].some((el) => el.textContent.includes("sourdough")));
  check("full-text search finds content", true);

  // 6. Deep link restores a document
  await page.goto(`${base}/#/doc/findme.md`);
  await page.waitForSelector(".block-h1");
  check("deep link opens the document",
    (await page.$eval("#document-title", (el) => el.value)) === "findme.md");

  // 7. Delete and restore from the trash view (only files with history are recoverable)
  await api("/api/file", "PUT", { path: "findme.md", content: "# Findme\n\nthe sourdough secret\n", clientId: "e2e-h" });
  await api("/api/file?path=findme.md", "DELETE");
  await page.goto(`${base}/`);
  await page.waitForSelector(".docs-row");
  await page.click("#docs-trash");
  await page.waitForSelector(".trash-row");
  await page.click(".trash-row .docs-row-action");
  await page.waitForFunction(() =>
    [...document.querySelectorAll(".docs-row-name")].some((el) => el.textContent === "findme.md"));
  check("deleted file restored from trash", true);

  check("no page errors", pageErrors.length === 0, pageErrors.join("; "));
} catch (error) {
  failures++;
  console.error("FAIL suite aborted:", error.message);
} finally {
  await browser.close();
  server.kill();
  rmSync(workspace, { recursive: true, force: true });
}

if (failures > 0) {
  console.error(`\n${failures} failure(s)`);
  process.exit(1);
}
console.log("\nall e2e checks passed");
