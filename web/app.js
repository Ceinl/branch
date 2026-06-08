const state = {
  root: null,
  directory: "",
  items: [],
  filter: "",
  file: null,
  content: "",
  dirty: false,
  saving: false,
  saveTimer: null,
  activeBlock: null,
  applying: false,
  view: "docs",
};

const els = {
  docsTopbar: document.getElementById("docs-topbar"),
  editorTopbar: document.getElementById("editor-topbar"),
  docsView: document.getElementById("docs-view"),
  editorView: document.getElementById("editor-view"),
  docsRootPath: document.getElementById("docs-root-path"),
  rootPath: document.getElementById("root-path"),
  docsSearch: document.getElementById("docs-search"),
  docsBreadcrumbs: document.getElementById("docs-breadcrumbs"),
  docsList: document.getElementById("docs-list"),
  homeNewDoc: document.getElementById("home-new-doc"),
  homeNewFolder: document.getElementById("home-new-folder"),
  backToDocs: document.getElementById("back-to-docs"),
  fileMenuToggle: document.getElementById("file-menu-toggle"),
  fileMenu: document.getElementById("file-menu"),
  fileNewDoc: document.getElementById("file-new-doc"),
  fileNewFolder: document.getElementById("file-new-folder"),
  fileOpenDocs: document.getElementById("file-open-docs"),
  fileSave: document.getElementById("file-save"),
  formatToggle: document.getElementById("format-toggle"),
  formatPanel: document.getElementById("format-panel"),
  title: document.getElementById("document-title"),
  editor: document.getElementById("editor"),
  saveState: document.getElementById("save-state"),
  toast: document.getElementById("toast"),
};

const token = new URLSearchParams(location.search).get("token") || getCookie("branch_token") || "";
if (token && location.search.includes("token=")) {
  history.replaceState(null, "", location.pathname);
}

init().catch((error) => showError(error.message));

async function init() {
  bindEvents();
  const root = await api("/api/root");
  state.root = root;
  els.docsRootPath.textContent = root.path;
  els.rootPath.textContent = root.path;
  await loadDirectory("");
  showDocsView();
}

function bindEvents() {
  els.homeNewDoc.addEventListener("click", () => createFile());
  els.homeNewFolder.addEventListener("click", () => createFolder());
  els.fileNewDoc.addEventListener("click", () => createFile());
  els.fileNewFolder.addEventListener("click", () => createFolder());
  els.fileOpenDocs.addEventListener("click", () => goToDocs());
  els.fileSave.addEventListener("click", () => saveNow());
  els.backToDocs.addEventListener("click", () => goToDocs());
  els.fileMenuToggle.addEventListener("click", () => toggleFileMenu());
  els.formatToggle.addEventListener("click", () => toggleFormatPanel());

  els.docsSearch.addEventListener("input", () => {
    state.filter = els.docsSearch.value.trim().toLowerCase();
    renderDocsList();
  });

  els.editor.addEventListener("click", () => {
    if (!state.file && !els.editor.querySelector(".block")) {
      insertBlock("p", "", null).focus();
    }
  });

  document.querySelectorAll("[data-block]").forEach((button) => {
    button.addEventListener("click", () => setActiveBlockType(button.dataset.block));
  });

  document.querySelectorAll("[data-inline]").forEach((button) => {
    button.addEventListener("click", () => applyInlineCommand(button.dataset.inline));
  });

  document.addEventListener("click", (event) => {
    if (!event.target.closest("#file-menu") && !event.target.closest("#file-menu-toggle")) {
      closeFileMenu();
    }
  });

  document.addEventListener("keydown", (event) => {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "s") {
      event.preventDefault();
      saveNow();
    }
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "o") {
      event.preventDefault();
      goToDocs();
    }
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "n") {
      event.preventDefault();
      createFile();
    }
    if (event.key === "Escape") {
      closeFileMenu();
      closeFormatPanel();
    }
  });
}

function toggleFileMenu() {
  const willOpen = els.fileMenu.hidden;
  closeFormatPanel();
  els.fileMenu.hidden = !willOpen;
  els.fileMenuToggle.classList.toggle("active", willOpen);
  els.fileMenuToggle.setAttribute("aria-expanded", String(willOpen));
}

function closeFileMenu() {
  els.fileMenu.hidden = true;
  els.fileMenuToggle.classList.remove("active");
  els.fileMenuToggle.setAttribute("aria-expanded", "false");
}

function toggleFormatPanel() {
  const willOpen = els.formatPanel.hidden;
  closeFileMenu();
  els.formatPanel.hidden = !willOpen;
  els.formatToggle.classList.toggle("active", willOpen);
}

function closeFormatPanel() {
  els.formatPanel.hidden = true;
  els.formatToggle.classList.remove("active");
}

function showDocsView() {
  state.view = "docs";
  closeFileMenu();
  closeFormatPanel();
  els.docsTopbar.hidden = false;
  els.docsView.hidden = false;
  els.editorTopbar.hidden = true;
  els.editorView.hidden = true;
  document.title = "Branch Docs";
}

function showEditorView() {
  state.view = "editor";
  closeFileMenu();
  els.docsTopbar.hidden = true;
  els.docsView.hidden = true;
  els.editorTopbar.hidden = false;
  els.editorView.hidden = false;
  document.title = state.file ? `${state.file.name} - Branch` : "Branch";
}

async function goToDocs() {
  try {
    await maybeSaveBeforeNavigation();
    const path = state.file ? dirname(state.file.path) : state.directory;
    await loadDirectory(path);
    showDocsView();
  } catch (error) {
    showError(error.message);
  }
}

async function loadDirectory(path) {
  const data = await api(`/api/files?path=${encodeURIComponent(path)}`);
  state.directory = data.path || "";
  state.items = data.items || [];
  renderDocsBreadcrumbs();
  renderDocsList();
}

function renderDocsBreadcrumbs() {
  els.docsBreadcrumbs.innerHTML = "";
  els.docsBreadcrumbs.append(crumbButton("Root", ""));
  const parts = state.directory ? state.directory.split("/") : [];
  let path = "";
  parts.forEach((part) => {
    path = path ? `${path}/${part}` : part;
    els.docsBreadcrumbs.append(crumbButton(part, path));
  });
}

function crumbButton(label, path) {
  const button = document.createElement("button");
  button.className = "crumb";
  button.type = "button";
  button.textContent = label;
  button.addEventListener("click", async () => {
    try {
      await maybeSaveBeforeNavigation();
      await loadDirectory(path);
      showDocsView();
    } catch (error) {
      showError(error.message);
    }
  });
  return button;
}

function renderDocsList() {
  els.docsList.innerHTML = "";
  const filter = state.filter;
  let items = state.items;
  if (filter) {
    items = items.filter((item) => item.name.toLowerCase().includes(filter) || item.path.toLowerCase().includes(filter));
  }
  if (state.directory && !filter) {
    const upPath = state.directory.split("/").slice(0, -1).join("/");
    els.docsList.append(docsRow({ name: "..", path: upPath, kind: "directory", modified: "", extension: "" }, true));
  }
  if (!items.length) {
    const empty = document.createElement("div");
    empty.className = "empty-docs";
    empty.textContent = filter ? "No matching documents." : "No documents in this folder.";
    els.docsList.append(empty);
    return;
  }
  items.forEach((item) => els.docsList.append(docsRow(item, false)));
}

function docsRow(item, isParent) {
  const button = document.createElement("button");
  button.className = "docs-row";
  button.type = "button";
  const isDirectory = item.kind === "directory";
  const icon = isDirectory ? "DIR" : item.markdown ? "MD" : "TXT";
  const type = isParent ? "Parent folder" : isDirectory ? "Folder" : item.markdown ? "Markdown" : item.extension || "Text";
  const modified = isParent ? "" : formatDate(item.modified);
  button.innerHTML = `
    <span class="docs-row-main">
      <span class="docs-row-icon${isDirectory ? " folder" : ""}">${icon}</span>
      <span class="docs-row-name">${escapeHTML(item.name)}</span>
    </span>
    <span class="docs-row-meta">${escapeHTML(type)}</span>
    <span class="docs-row-meta">${escapeHTML(modified)}</span>
  `;
  button.addEventListener("click", async () => {
    try {
      await maybeSaveBeforeNavigation();
      if (isDirectory) {
        await loadDirectory(item.path);
        showDocsView();
      } else {
        await loadFile(item.path);
      }
    } catch (error) {
      showError(error.message);
    }
  });
  return button;
}

async function maybeSaveBeforeNavigation() {
  if (state.dirty) {
    await saveNow();
  }
}

async function loadFile(path) {
  const file = await api(`/api/file?path=${encodeURIComponent(path)}`);
  state.file = file;
  state.content = file.content || "";
  state.dirty = false;
  els.title.value = file.name || basename(file.path);
  els.rootPath.textContent = file.path;
  renderMarkdownDocument(state.content);
  setSaveState("Saved", "saved");
  await loadDirectory(dirname(file.path));
  showEditorView();
}

async function createFile() {
  closeFileMenu();
  const name = prompt("New Markdown filename", uniqueDocName());
  if (!name) return;
  const path = joinPath(state.directory, ensureMarkdownName(name.trim()));
  try {
    await maybeSaveBeforeNavigation();
    await api("/api/file", {
      method: "POST",
      body: JSON.stringify({ path, kind: "file", content: "# Untitled\n\nStart writing here.\n" }),
    });
    await loadFile(path);
    showToast("Created " + path);
  } catch (error) {
    showError(error.message);
  }
}

async function createFolder() {
  closeFileMenu();
  const name = prompt("New folder name", "notes");
  if (!name) return;
  const path = joinPath(state.directory, name.trim());
  try {
    await maybeSaveBeforeNavigation();
    await api("/api/file", {
      method: "POST",
      body: JSON.stringify({ path, kind: "directory" }),
    });
    await loadDirectory(state.directory);
    if (state.view === "docs") showDocsView();
    showToast("Created folder " + path);
  } catch (error) {
    showError(error.message);
  }
}

function renderMarkdownDocument(markdown) {
  state.applying = true;
  els.editor.innerHTML = "";
  const blocks = parseMarkdownBlocks(markdown || "");
  if (!blocks.length) {
    insertBlock("p", "", null);
  } else {
    blocks.forEach((block) => insertBlock(block.type, block.html, null, block.checked));
  }
  state.applying = false;
  const first = els.editor.querySelector(".block");
  if (first) {
    first.focus();
    setCaretToEnd(first);
  }
}

function parseMarkdownBlocks(markdown) {
  const lines = markdown.replace(/\r\n/g, "\n").split("\n");
  const blocks = [];
  let inCode = false;
  let code = [];

  for (const rawLine of lines) {
    const line = rawLine;
    const trimmed = line.trim();
    if (trimmed.startsWith("```")) {
      if (inCode) {
        blocks.push({ type: "code", html: escapeHTML(code.join("\n")) });
        code = [];
        inCode = false;
      } else {
        inCode = true;
      }
      continue;
    }
    if (inCode) {
      code.push(line);
      continue;
    }
    if (!trimmed) {
      continue;
    }
    const heading = trimmed.match(/^(#{1,6})\s+(.+)$/);
    if (heading) {
      blocks.push({ type: `h${heading[1].length}`, html: inlineMarkdown(heading[2]) });
      continue;
    }
    const task = trimmed.match(/^[-*]\s+\[([ xX])\]\s+(.+)$/);
    if (task) {
      blocks.push({ type: "task", html: inlineMarkdown(task[2]), checked: task[1].toLowerCase() === "x" });
      continue;
    }
    const bullet = trimmed.match(/^[-*]\s+(.+)$/);
    if (bullet) {
      blocks.push({ type: "bullet", html: inlineMarkdown(bullet[1]) });
      continue;
    }
    const numbered = trimmed.match(/^\d+\.\s+(.+)$/);
    if (numbered) {
      blocks.push({ type: "numbered", html: inlineMarkdown(numbered[1]) });
      continue;
    }
    const quote = trimmed.match(/^>\s?(.*)$/);
    if (quote) {
      blocks.push({ type: "quote", html: inlineMarkdown(quote[1]) });
      continue;
    }
    blocks.push({ type: "p", html: inlineMarkdown(trimmed) });
  }
  if (inCode) {
    blocks.push({ type: "code", html: escapeHTML(code.join("\n")) });
  }
  return blocks;
}

function insertBlock(type, html = "", after = null, checked = false) {
  const block = document.createElement("div");
  block.className = `block block-${type}`;
  block.dataset.type = type;
  block.dataset.placeholder = placeholderForType(type);
  block.contentEditable = "true";
  block.spellcheck = type !== "code";
  if (checked) {
    block.classList.add("checked");
  }
  block.innerHTML = html || "";
  bindBlock(block);
  if (after) {
    after.insertAdjacentElement("afterend", block);
  } else {
    els.editor.append(block);
  }
  return block;
}

function bindBlock(block) {
  block.addEventListener("focus", () => {
    state.activeBlock = block;
  });
  block.addEventListener("keydown", (event) => handleBlockKeydown(event, block));
  block.addEventListener("input", () => handleBlockInput(block));
  block.addEventListener("paste", (event) => pastePlainText(event));
}

function handleBlockKeydown(event, block) {
  if (event.key === "Enter" && !event.shiftKey) {
    event.preventDefault();
    if (["bullet", "numbered", "task"].includes(block.dataset.type) && isBlockEmpty(block)) {
      setBlockType(block, "p");
      markChanged();
      return;
    }
    const transformed = transformCompleteBlockSyntax(block);
    const nextType = nextBlockType(block.dataset.type);
    const next = insertBlock(nextType, "", transformed || block);
    next.focus();
    setCaretToEnd(next);
    markChanged();
    return;
  }

  if (event.key === "Backspace" && isBlockEmpty(block)) {
    if (block.dataset.type !== "p") {
      event.preventDefault();
      setBlockType(block, "p");
      markChanged();
      return;
    }
    const previous = block.previousElementSibling;
    if (previous) {
      event.preventDefault();
      block.remove();
      previous.focus();
      setCaretToEnd(previous);
      markChanged();
    }
  }
}

function nextBlockType(type) {
  if (["bullet", "numbered", "task"].includes(type)) return type;
  return "p";
}

function handleBlockInput(block) {
  if (state.applying) return;
  transformLeadingShortcut(block);
  transformInlineTextNodes(block);
  markChanged();
}

function transformLeadingShortcut(block) {
  if (block.dataset.type !== "p") return false;
  const text = normalizedText(block);
  const shortcut = [
    [/^#\s$/, "h1"],
    [/^##\s$/, "h2"],
    [/^###\s$/, "h3"],
    [/^####\s$/, "h4"],
    [/^>\s$/, "quote"],
    [/^[-*]\s$/, "bullet"],
    [/^\d+\.\s$/, "numbered"],
    [/^-\s+\[\s\]\s$/, "task"],
    [/^```$/, "code"],
  ].find(([pattern]) => pattern.test(text));
  if (!shortcut) return false;
  state.applying = true;
  setBlockType(block, shortcut[1]);
  block.innerHTML = "";
  state.applying = false;
  block.focus();
  setCaretToEnd(block);
  return true;
}

function transformCompleteBlockSyntax(block) {
  if (block.dataset.type !== "p") return null;
  const text = normalizedText(block).trim();
  const heading = text.match(/^(#{1,6})\s+(.+)$/);
  if (heading) {
    setBlockType(block, `h${heading[1].length}`);
    block.innerHTML = inlineMarkdown(heading[2]);
    return block;
  }
  const quote = text.match(/^>\s+(.+)$/);
  if (quote) {
    setBlockType(block, "quote");
    block.innerHTML = inlineMarkdown(quote[1]);
    return block;
  }
  const bullet = text.match(/^[-*]\s+(.+)$/);
  if (bullet) {
    setBlockType(block, "bullet");
    block.innerHTML = inlineMarkdown(bullet[1]);
    return block;
  }
  return null;
}

function transformInlineTextNodes(block) {
  if (block.dataset.type === "code") return;
  const walker = document.createTreeWalker(block, NodeFilter.SHOW_TEXT, {
    acceptNode(node) {
      const parent = node.parentElement;
      if (!parent || parent.closest("strong, em, code, s, a")) {
        return NodeFilter.FILTER_REJECT;
      }
      return hasInlineMarkdown(node.nodeValue) ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_REJECT;
    },
  });
  const nodes = [];
  while (walker.nextNode()) {
    nodes.push(walker.currentNode);
  }
  if (!nodes.length) return;
  state.applying = true;
  nodes.forEach((node) => {
    const fragment = inlineMarkdownFragment(node.nodeValue);
    node.replaceWith(fragment);
  });
  state.applying = false;
  block.focus();
  setCaretToEnd(block);
}

function hasInlineMarkdown(text) {
  return /\*\*[^*\n]+\*\*|__[^_\n]+__|`[^`\n]+`|~~[^~\n]+~~|\[[^\]\n]+\]\([^\s)]+\)|(^|\s)\*[^*\n]+\*/.test(text);
}

function inlineMarkdownFragment(text) {
  const fragment = document.createDocumentFragment();
  const pattern = /(\*\*([^*\n]+)\*\*)|(__([^_\n]+)__)|(`([^`\n]+)`)|(~~([^~\n]+)~~)|(\[([^\]\n]+)\]\(([^\s)]+)\))|((^|\s)\*([^*\n]+)\*)/g;
  let index = 0;
  let match;
  while ((match = pattern.exec(text))) {
    if (match.index > index) {
      fragment.append(document.createTextNode(text.slice(index, match.index)));
    }
    if (match[2]) {
      fragment.append(wrapElement("strong", match[2]));
    } else if (match[4]) {
      fragment.append(wrapElement("strong", match[4]));
    } else if (match[6]) {
      fragment.append(wrapElement("code", match[6]));
    } else if (match[8]) {
      fragment.append(wrapElement("s", match[8]));
    } else if (match[10]) {
      const safeURL = sanitizeURL(match[11]);
      if (safeURL) {
        const link = wrapElement("a", match[10]);
        link.href = safeURL;
        link.target = "_blank";
        link.rel = "noreferrer";
        fragment.append(link);
      } else {
        fragment.append(document.createTextNode(match[10]));
      }
    } else if (match[14]) {
      fragment.append(document.createTextNode(match[13] || ""));
      fragment.append(wrapElement("em", match[14]));
    }
    index = match.index + match[0].length;
  }
  if (index < text.length) {
    fragment.append(document.createTextNode(text.slice(index)));
  }
  return fragment;
}

function wrapElement(tag, text) {
  const el = document.createElement(tag);
  el.textContent = text;
  return el;
}

function setActiveBlockType(type) {
  const block = state.activeBlock || els.editor.querySelector(".block");
  if (!block) return;
  setBlockType(block, type);
  block.focus();
  setCaretToEnd(block);
  markChanged();
}

function setBlockType(block, type) {
  block.dataset.type = type;
  block.dataset.placeholder = placeholderForType(type);
  block.spellcheck = type !== "code";
  block.className = `block block-${type}`;
}

function applyInlineCommand(command) {
  const block = state.activeBlock || els.editor.querySelector(".block");
  if (!block) return;
  block.focus();
  if (command === "bold") {
    document.execCommand("bold", false);
  } else if (command === "italic") {
    document.execCommand("italic", false);
  } else if (command === "code") {
    wrapSelectionWith("code");
  }
  markChanged();
}

function wrapSelectionWith(tag) {
  const selection = window.getSelection();
  if (!selection || selection.rangeCount === 0 || selection.isCollapsed) return;
  const range = selection.getRangeAt(0);
  const wrapper = document.createElement(tag);
  wrapper.append(range.extractContents());
  range.insertNode(wrapper);
  selection.removeAllRanges();
  const nextRange = document.createRange();
  nextRange.selectNodeContents(wrapper);
  selection.addRange(nextRange);
}

function markChanged() {
  if (!state.file) return;
  state.content = serializeDocument();
  state.dirty = true;
  setSaveState("Editing", "dirty");
  scheduleSave();
}

function scheduleSave() {
  clearTimeout(state.saveTimer);
  state.saveTimer = setTimeout(() => saveNow().catch((error) => showError(error.message)), 900);
}

async function saveNow() {
  clearTimeout(state.saveTimer);
  closeFileMenu();
  if (!state.file) {
    setSaveState("Open or create a file", "error");
    return;
  }
  const content = serializeDocument();
  if (!state.dirty && content === state.content) {
    setSaveState("Saved", "saved");
    return;
  }
  state.saving = true;
  setSaveState("Saving", "dirty");
  const result = await api("/api/file", {
    method: "PUT",
    body: JSON.stringify({ path: state.file.path, content }),
  });
  state.file.modified = result.modified;
  state.file.size = result.size;
  state.content = content;
  state.dirty = false;
  state.saving = false;
  setSaveState("Saved", "saved");
  loadDirectory(state.directory).catch(() => {});
}

function serializeDocument() {
  const blocks = [...els.editor.querySelectorAll(".block")];
  const lines = blocks.map((block) => serializeBlock(block));
  return lines.join("\n\n").replace(/[ \t]+\n/g, "\n").trimEnd() + "\n";
}

function serializeBlock(block) {
  const type = block.dataset.type || "p";
  const text = type === "code" ? block.textContent.replace(/\u00a0/g, " ") : inlineHTMLToMarkdown(block).trim();
  switch (type) {
    case "h1":
      return `# ${text}`.trimEnd();
    case "h2":
      return `## ${text}`.trimEnd();
    case "h3":
      return `### ${text}`.trimEnd();
    case "h4":
      return `#### ${text}`.trimEnd();
    case "h5":
      return `##### ${text}`.trimEnd();
    case "h6":
      return `###### ${text}`.trimEnd();
    case "quote":
      return `> ${text}`.trimEnd();
    case "bullet":
      return `- ${text}`.trimEnd();
    case "numbered":
      return `1. ${text}`.trimEnd();
    case "task":
      return `- [${block.classList.contains("checked") ? "x" : " "}] ${text}`.trimEnd();
    case "code":
      return `\`\`\`\n${text}\n\`\`\``;
    default:
      return text;
  }
}

function inlineHTMLToMarkdown(node) {
  let output = "";
  node.childNodes.forEach((child) => {
    if (child.nodeType === Node.TEXT_NODE) {
      output += child.nodeValue.replace(/\u00a0/g, " ");
      return;
    }
    if (child.nodeType !== Node.ELEMENT_NODE) return;
    const tag = child.tagName.toLowerCase();
    const inner = inlineHTMLToMarkdown(child);
    if (tag === "strong" || tag === "b") {
      output += `**${inner}**`;
    } else if (tag === "em" || tag === "i") {
      output += `*${inner}*`;
    } else if (tag === "code") {
      output += `\`${inner}\``;
    } else if (tag === "s" || tag === "strike") {
      output += `~~${inner}~~`;
    } else if (tag === "a") {
      output += `[${inner}](${child.getAttribute("href") || ""})`;
    } else if (tag === "br") {
      output += "\n";
    } else {
      output += inner;
    }
  });
  return output;
}

function inlineMarkdown(text) {
  return fragmentToHTML(inlineMarkdownFragment(text));
}

function fragmentToHTML(fragment) {
  const div = document.createElement("div");
  div.append(fragment);
  return div.innerHTML;
}

function placeholderForType(type) {
  if (type === "h1") return "Heading 1";
  if (type === "h2") return "Heading 2";
  if (type === "h3") return "Heading 3";
  if (type === "quote") return "Quote";
  if (type === "bullet") return "List item";
  if (type === "numbered") return "Numbered item";
  if (type === "task") return "Task";
  if (type === "code") return "Code";
  return "Type / or Markdown";
}

function pastePlainText(event) {
  event.preventDefault();
  const text = event.clipboardData?.getData("text/plain") || "";
  document.execCommand("insertText", false, text);
}

function isBlockEmpty(block) {
  return normalizedText(block).trim() === "";
}

function normalizedText(block) {
  return block.textContent.replace(/\u00a0/g, " ");
}

function setCaretToEnd(element) {
  const selection = window.getSelection();
  if (!selection) return;
  const range = document.createRange();
  range.selectNodeContents(element);
  range.collapse(false);
  selection.removeAllRanges();
  selection.addRange(range);
}

function setSaveState(text, className) {
  els.saveState.textContent = text;
  els.saveState.className = `save-state ${className || ""}`.trim();
}

async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: {
      "Content-Type": "application/json",
      "X-Branch-Token": token,
      ...(options.headers || {}),
    },
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(data.error || `${response.status} ${response.statusText}`);
  }
  return data;
}

function getCookie(name) {
  return document.cookie
    .split(";")
    .map((part) => part.trim())
    .find((part) => part.startsWith(name + "="))
    ?.slice(name.length + 1);
}

function dirname(path) {
  const parts = path.split("/");
  parts.pop();
  return parts.join("/");
}

function basename(path) {
  return path.split("/").filter(Boolean).pop() || path;
}

function joinPath(base, name) {
  return [base, name].filter(Boolean).join("/").replace(/\/+/g, "/");
}

function ensureMarkdownName(name) {
  return /\.m(?:d|arkdown|down|kd)$/i.test(name) ? name : `${name}.md`;
}

function uniqueDocName() {
  const now = new Date();
  const stamp = [now.getFullYear(), String(now.getMonth() + 1).padStart(2, "0"), String(now.getDate()).padStart(2, "0")].join("-");
  return `note-${stamp}.md`;
}

function formatDate(value) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(date);
}

function sanitizeURL(url) {
  try {
    const parsed = new URL(url, location.origin);
    if (["http:", "https:", "mailto:"].includes(parsed.protocol)) {
      return parsed.href;
    }
  } catch (_) {
    return "";
  }
  return "";
}

function escapeHTML(value) {
  return String(value)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#039;");
}

function showError(message) {
  if (!els.editorTopbar.hidden) {
    setSaveState("Error", "error");
  }
  showToast(message || "Something went wrong");
}

let toastTimer = null;
function showToast(message) {
  clearTimeout(toastTimer);
  els.toast.textContent = message;
  els.toast.classList.add("show");
  toastTimer = setTimeout(() => els.toast.classList.remove("show"), 2800);
}
