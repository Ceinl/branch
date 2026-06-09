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
  user: null,
  clientId: loadClientId(),
  stream: null,
  remotePending: null,
  remoteClients: new Map(),
  localDirtyBlocks: new Set(),
  collabVersion: 0,
  collabTimer: null,
  presenceTimer: null,
  presenceHeartbeatTimer: null,
  collabFallbackTimer: null,
  collabPollTimer: null,
  collabPolling: false,
  streamReady: false,
  remoteCleanupTimer: null,
  config: null,
  historyOpen: false,
  historyNodes: [],
  historySelected: null,
  historyTab: "changes",
};

const els = {
  authView: document.getElementById("auth-view"),
  signinButton: document.getElementById("signin-button"),
  authDetail: document.getElementById("auth-detail"),
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
  signoutButton: document.getElementById("signout-button"),
  backToDocs: document.getElementById("back-to-docs"),
  fileMenuToggle: document.getElementById("file-menu-toggle"),
  fileMenu: document.getElementById("file-menu"),
  fileNewDoc: document.getElementById("file-new-doc"),
  fileNewFolder: document.getElementById("file-new-folder"),
  fileOpenDocs: document.getElementById("file-open-docs"),
  fileSave: document.getElementById("file-save"),
  fileRename: document.getElementById("file-rename"),
  fileDelete: document.getElementById("file-delete"),
  collabPresence: document.getElementById("collab-presence"),
  formatToggle: document.getElementById("format-toggle"),
  formatPanel: document.getElementById("format-panel"),
  pageCard: document.getElementById("page-card"),
  remoteCursors: document.getElementById("remote-cursors"),
  title: document.getElementById("document-title"),
  editor: document.getElementById("editor"),
  saveState: document.getElementById("save-state"),
  toast: document.getElementById("toast"),
  fileHistory: document.getElementById("file-history"),
  historyToggle: document.getElementById("history-toggle"),
  historyPanel: document.getElementById("history-panel"),
  historyClose: document.getElementById("history-close"),
  historyGraph: document.getElementById("history-graph"),
  historyEmpty: document.getElementById("history-empty"),
  historyPreview: document.getElementById("history-preview"),
  historyPreviewBody: document.getElementById("history-preview-body"),
  historyRestore: document.getElementById("history-restore"),
  historyName: document.getElementById("history-name"),
  historyTabChanges: document.getElementById("history-tab-changes"),
  historyTabDocument: document.getElementById("history-tab-document"),
};

init().catch((error) => showError(error.message));

function loadClientId() {
  // Keep this page-scoped so duplicated tabs never ignore each other as the same collaborator.
  return randomClientId();
}

function randomClientId() {
  if (window.crypto?.randomUUID) {
    return window.crypto.randomUUID();
  }
  const bytes = new Uint8Array(16);
  if (window.crypto?.getRandomValues) {
    window.crypto.getRandomValues(bytes);
  } else {
    for (let i = 0; i < bytes.length; i++) {
      bytes[i] = Math.floor(Math.random() * 256);
    }
  }
  return [...bytes].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

async function init() {
  bindEvents();
  state.config = await loadConfig();
  configureShoo();
  const signedIn = await establishSession();
  if (!signedIn) {
    showAuthView();
    return;
  }
  const root = await api("/api/root");
  state.root = root;
  els.docsRootPath.textContent = root.path;
  els.rootPath.textContent = root.path;
  await loadDirectory("");
  showDocsView();
}

async function loadConfig() {
  const response = await fetch("/api/config", { credentials: "same-origin" });
  const config = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(config.error || "Failed to load Branch config");
  }
  return config;
}

function configureShoo() {
  els.signoutButton.hidden = !state.config?.authRequired;
  if (!window.Shoo || !state.config?.redirectURI) return;
  window.Shoo.defaults.redirectUri = state.config.redirectURI;
  window.Shoo.defaults.clientId = `origin:${new URL(state.config.redirectURI).origin}`;
}

function bindEvents() {
  els.signinButton.addEventListener("click", () => signIn());
  els.signoutButton.addEventListener("click", () => signOut());
  els.homeNewDoc.addEventListener("click", () => createFile());
  els.homeNewFolder.addEventListener("click", () => createFolder());
  els.fileNewDoc.addEventListener("click", () => createFile());
  els.fileNewFolder.addEventListener("click", () => createFolder());
  els.fileOpenDocs.addEventListener("click", () => goToDocs());
  els.fileSave.addEventListener("click", () => saveNow());
  els.fileRename.addEventListener("click", () => renameCurrentFile().catch((error) => showError(error.message)));
  els.fileDelete.addEventListener("click", () => deleteCurrentFile().catch((error) => showError(error.message)));
  els.backToDocs.addEventListener("click", () => goToDocs());
  els.fileMenuToggle.addEventListener("click", () => toggleFileMenu());
  els.formatToggle.addEventListener("click", () => toggleFormatPanel());
  els.fileHistory.addEventListener("click", () => toggleHistoryPanel());
  els.historyToggle.addEventListener("click", () => toggleHistoryPanel());
  els.historyClose.addEventListener("click", () => closeHistoryPanel());
  els.historyRestore.addEventListener("click", () => restoreSelectedVersion());
  els.historyName.addEventListener("click", () => nameSelectedVersion().catch((error) => showError(error.message)));
  els.historyTabChanges.addEventListener("click", () => setHistoryTab("changes"));
  els.historyTabDocument.addEventListener("click", () => setHistoryTab("document"));

  els.docsSearch.addEventListener("input", () => {
    state.filter = els.docsSearch.value.trim().toLowerCase();
    renderDocsList();
  });

  els.editor.addEventListener("click", () => {
    if (!state.file && !els.editor.querySelector(".block")) {
      insertBlock("p", "", null).focus();
    }
    scheduleCollabPresence();
  });
  els.editor.addEventListener("keyup", () => scheduleCollabPresence());
  els.editor.addEventListener("mouseup", () => scheduleCollabPresence());

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
    if ((event.metaKey || event.ctrlKey) && event.shiftKey && event.key.toLowerCase() === "h") {
      event.preventDefault();
      toggleHistoryPanel();
    }
    if (event.key === "Escape") {
      closeFileMenu();
      closeFormatPanel();
      closeHistoryPanel();
    }
  });

  document.addEventListener("selectionchange", () => scheduleCollabPresence());
  window.addEventListener("resize", () => renderRemoteCursors());
  document.addEventListener("scroll", () => renderRemoteCursors(), true);
}

async function establishSession() {
  if (!state.config?.authRequired) {
    state.user = { id: "local", name: "Local user" };
    showAppShell();
    return true;
  }
  try {
    const existing = await sessionRequest("/api/session");
    state.user = existing.user;
    showAppShell();
    return true;
  } catch (_) {
    // Fall back to Shoo's stored identity below.
  }
  if (!window.Shoo) {
    els.authDetail.textContent = "Shoo script did not load. Check network access to https://shoo.dev/.";
    return false;
  }
  if (location.pathname === "/shoo/callback") {
    els.authDetail.textContent = "Finishing Shoo sign-in...";
    return false;
  }
  const identity = window.Shoo.getIdentity();
  if (!identity || !identity.token) {
    return false;
  }
  try {
    const session = await sessionRequest("/api/session", {
      method: "POST",
      body: JSON.stringify({ idToken: identity.token }),
    });
    state.user = session.user;
    showAppShell();
    return true;
  } catch (error) {
    window.Shoo.clearIdentity();
    els.authDetail.textContent = error.message || "Sign-in verification failed.";
    return false;
  }
}

function signIn() {
  if (!window.Shoo) {
    showError("Shoo script is not available");
    return;
  }
  startShooSignIn().catch((error) => showError(error.message));
}

async function startShooSignIn() {
  const options = shooSignInOptions();
  if (state.config?.origin && state.config.origin !== window.location.origin) {
    window.location.assign(state.config.origin + "/");
    return;
  }
  validateShooRedirect(options.redirectUri);
  if (window.crypto?.subtle?.digest) {
    await window.Shoo.startSignIn(options);
    return;
  }
  startShooSignInWithoutSubtle(options);
}

function shooSignInOptions() {
  const redirectUri = state.config?.redirectURI || new URL("/shoo/callback", window.location.origin).toString();
  return {
    returnTo: "/",
    requestPii: true,
    redirectUri,
    clientId: `origin:${new URL(redirectUri).origin}`,
  };
}

function validateShooRedirect(redirectUri) {
  const parsed = new URL(redirectUri);
  if (parsed.protocol === "https:") return;
  if (parsed.protocol === "http:" && parsed.hostname === "localhost") return;
  throw new Error(`Shoo requires HTTPS, or http://localhost for development. Open ${state.config?.origin || "http://localhost"} instead.`);
}

function startShooSignInWithoutSubtle(options = {}) {
  const defaults = window.Shoo?.defaults || {};
  const redirectUri = options.redirectUri || defaults.redirectUri || new URL("/shoo/callback", window.location.origin).toString();
  const callbackPath = new URL(redirectUri).pathname || defaults.callbackPath || "/shoo/callback";
  const clientId = options.clientId || defaults.clientId || `origin:${new URL(redirectUri).origin}`;
  const shooBaseUrl = defaults.shooBaseUrl || "https://shoo.dev";
  const state = randomPKCEString(32);
  const verifier = randomPKCEString(64);
  const challenge = base64URL(sha256Bytes(asciiBytes(verifier)));

  sessionStorage.setItem(defaults.pkceStorageKey || "shoo_pkce", JSON.stringify({ state, verifier, createdAt: Date.now() }));
  let returnTo = normalizeReturnTo(options.returnTo || "/");
  if (returnTo === callbackPath) {
    returnTo = "/";
  }
  sessionStorage.setItem(defaults.returnToStorageKey || "shoo_return_to", returnTo);

  const url = new URL("/authorize", shooBaseUrl);
  url.searchParams.set("client_id", clientId);
  url.searchParams.set("redirect_uri", redirectUri);
  url.searchParams.set("state", state);
  url.searchParams.set("code_challenge", challenge);
  url.searchParams.set("code_challenge_method", "S256");
  if (options.requestPii) {
    url.searchParams.set("pii", "true");
  }
  window.location.assign(url.toString());
}

function randomPKCEString(length) {
  if (!window.crypto?.getRandomValues) {
    throw new Error("Shoo sign-in requires browser crypto random values. Use a modern browser.");
  }
  const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~";
  const random = new Uint8Array(length);
  window.crypto.getRandomValues(random);
  let out = "";
  for (const value of random) {
    out += chars[value % chars.length];
  }
  return out;
}

function normalizeReturnTo(value) {
  try {
    const parsed = new URL(value || "/", window.location.origin);
    if (parsed.origin !== window.location.origin) return "/";
    const route = parsed.pathname + parsed.search + parsed.hash;
    return route.startsWith("/") && !route.startsWith("//") ? route : "/";
  } catch (_) {
    return "/";
  }
}

function asciiBytes(value) {
  const bytes = new Uint8Array(value.length);
  for (let i = 0; i < value.length; i += 1) {
    bytes[i] = value.charCodeAt(i) & 0xff;
  }
  return bytes;
}

function base64URL(bytes) {
  let binary = "";
  for (const value of bytes) {
    binary += String.fromCharCode(value);
  }
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function sha256Bytes(input) {
  const k = [
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
  ];
  let h0 = 0x6a09e667;
  let h1 = 0xbb67ae85;
  let h2 = 0x3c6ef372;
  let h3 = 0xa54ff53a;
  let h4 = 0x510e527f;
  let h5 = 0x9b05688c;
  let h6 = 0x1f83d9ab;
  let h7 = 0x5be0cd19;
  const total = (((input.length + 9 + 63) >> 6) << 6);
  const data = new Uint8Array(total);
  data.set(input);
  data[input.length] = 0x80;
  const bitLength = input.length * 8;
  data[total - 4] = (bitLength >>> 24) & 0xff;
  data[total - 3] = (bitLength >>> 16) & 0xff;
  data[total - 2] = (bitLength >>> 8) & 0xff;
  data[total - 1] = bitLength & 0xff;
  const w = new Uint32Array(64);
  for (let offset = 0; offset < total; offset += 64) {
    for (let i = 0; i < 16; i += 1) {
      const j = offset + i * 4;
      w[i] = ((data[j] << 24) | (data[j + 1] << 16) | (data[j + 2] << 8) | data[j + 3]) >>> 0;
    }
    for (let i = 16; i < 64; i += 1) {
      const s0 = rotr(w[i - 15], 7) ^ rotr(w[i - 15], 18) ^ (w[i - 15] >>> 3);
      const s1 = rotr(w[i - 2], 17) ^ rotr(w[i - 2], 19) ^ (w[i - 2] >>> 10);
      w[i] = (w[i - 16] + s0 + w[i - 7] + s1) >>> 0;
    }
    let a = h0;
    let b = h1;
    let c = h2;
    let d = h3;
    let e = h4;
    let f = h5;
    let g = h6;
    let h = h7;
    for (let i = 0; i < 64; i += 1) {
      const s1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25);
      const ch = (e & f) ^ (~e & g);
      const temp1 = (h + s1 + ch + k[i] + w[i]) >>> 0;
      const s0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22);
      const maj = (a & b) ^ (a & c) ^ (b & c);
      const temp2 = (s0 + maj) >>> 0;
      h = g;
      g = f;
      f = e;
      e = (d + temp1) >>> 0;
      d = c;
      c = b;
      b = a;
      a = (temp1 + temp2) >>> 0;
    }
    h0 = (h0 + a) >>> 0;
    h1 = (h1 + b) >>> 0;
    h2 = (h2 + c) >>> 0;
    h3 = (h3 + d) >>> 0;
    h4 = (h4 + e) >>> 0;
    h5 = (h5 + f) >>> 0;
    h6 = (h6 + g) >>> 0;
    h7 = (h7 + h) >>> 0;
  }
  const out = new Uint8Array(32);
  [h0, h1, h2, h3, h4, h5, h6, h7].forEach((value, i) => {
    out[i * 4] = (value >>> 24) & 0xff;
    out[i * 4 + 1] = (value >>> 16) & 0xff;
    out[i * 4 + 2] = (value >>> 8) & 0xff;
    out[i * 4 + 3] = value & 0xff;
  });
  return out;
}

function rotr(value, bits) {
  return (value >>> bits) | (value << (32 - bits));
}

async function signOut() {
  try {
    disconnectCollab();
    await sessionRequest("/api/session", { method: "DELETE" });
  } catch (_) {
    // Continue local sign-out even if the server session was already gone.
  }
  if (window.Shoo) {
    window.Shoo.clearIdentity();
  }
  state.user = null;
  state.file = null;
  state.content = "";
  state.dirty = false;
  showAuthView();
}

function showAuthView() {
  els.authView.hidden = false;
  els.docsTopbar.hidden = true;
  els.docsView.hidden = true;
  els.editorTopbar.hidden = true;
  els.editorView.hidden = true;
  if (state.config?.origin && state.config.origin !== window.location.origin) {
    els.authDetail.textContent = `Shoo requires sign-in from ${state.config.origin}. Click sign in to continue there.`;
  }
  document.title = "Sign in - Branch";
}

function showAppShell() {
  els.authView.hidden = true;
}

async function sessionRequest(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      ...(options.headers || {}),
    },
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(data.error || `${response.status} ${response.statusText}`);
  }
  return data;
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
  disconnectCollab();
  closeFileMenu();
  closeFormatPanel();
  closeHistoryPanel();
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
  const row = document.createElement("div");
  row.className = "docs-row";
  row.setAttribute("role", "button");
  row.tabIndex = 0;
  const isDirectory = item.kind === "directory";
  const icon = isDirectory ? "DIR" : item.markdown ? "MD" : "TXT";
  const type = isParent ? "Parent folder" : isDirectory ? "Folder" : item.markdown ? "Markdown" : item.extension || "Text";
  const modified = isParent ? "" : formatDate(item.modified);
  row.innerHTML = `
    <span class="docs-row-main">
      <span class="docs-row-icon${isDirectory ? " folder" : ""}">${icon}</span>
      <span class="docs-row-name">${escapeHTML(item.name)}</span>
    </span>
    <span class="docs-row-meta">${escapeHTML(type)}</span>
    <span class="docs-row-meta">${escapeHTML(modified)}</span>
  `;
  const open = async () => {
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
  };
  row.addEventListener("click", (event) => {
    if (event.target.closest(".docs-row-actions")) return;
    open();
  });
  row.addEventListener("keydown", (event) => {
    if (event.key === "Enter" && !event.target.closest(".docs-row-actions")) {
      event.preventDefault();
      open();
    }
  });
  if (!isParent) {
    const actions = document.createElement("span");
    actions.className = "docs-row-actions";
    if (!isDirectory) {
      const rename = document.createElement("button");
      rename.type = "button";
      rename.className = "docs-row-action";
      rename.textContent = "Rename";
      rename.addEventListener("click", () => renameItem(item).catch((error) => showError(error.message)));
      actions.append(rename);
    }
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "docs-row-action danger";
    remove.textContent = "Delete";
    remove.addEventListener("click", () => deleteItem(item).catch((error) => showError(error.message)));
    actions.append(remove);
    row.append(actions);
  }
  return row;
}

async function renameCurrentFile() {
  closeFileMenu();
  if (!state.file) return;
  await maybeSaveBeforeNavigation();
  const name = prompt("Rename to", basename(state.file.path));
  if (!name || name.trim() === basename(state.file.path)) return;
  const to = joinPath(dirname(state.file.path), name.trim());
  const result = await api("/api/file/rename", {
    method: "POST",
    body: JSON.stringify({ path: state.file.path, to }),
  });
  showToast(`Renamed to ${result.path}`);
  await loadFile(result.path);
}

async function deleteCurrentFile() {
  closeFileMenu();
  if (!state.file) return;
  if (!confirm(`Delete "${basename(state.file.path)}"? Its version history is kept.`)) return;
  const path = state.file.path;
  state.file = null;
  state.dirty = false;
  await api(`/api/file?path=${encodeURIComponent(path)}`, { method: "DELETE" });
  showToast(`Deleted ${basename(path)}`);
  await loadDirectory(dirname(path));
  showDocsView();
}

async function renameItem(item) {
  const name = prompt("Rename to", item.name);
  if (!name || name.trim() === item.name) return;
  const to = joinPath(dirname(item.path), name.trim());
  const result = await api("/api/file/rename", {
    method: "POST",
    body: JSON.stringify({ path: item.path, to }),
  });
  await loadDirectory(state.directory);
  showToast(`Renamed to ${result.path}`);
}

async function deleteItem(item) {
  const what = item.kind === "directory" ? "folder" : "file";
  if (!confirm(`Delete ${what} "${item.name}"?${item.kind === "directory" ? "" : " Its version history is kept."}`)) return;
  await api(`/api/file?path=${encodeURIComponent(item.path)}`, { method: "DELETE" });
  await loadDirectory(state.directory);
  showToast(`Deleted ${item.name}`);
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
  state.localDirtyBlocks.clear();
  els.title.value = file.name || basename(file.path);
  els.rootPath.textContent = file.path;
  renderMarkdownDocument(state.content);
  setSaveState("Saved", "saved");
  await loadDirectory(dirname(file.path));
  showEditorView();
  connectCollab(file.path);
  if (state.historyOpen) {
    state.historySelected = null;
    renderHistoryPreview(null);
    refreshHistory().catch(() => {});
  }
}

function connectCollab(path) {
  disconnectCollab();
  state.remotePending = null;
  state.remoteClients.clear();
  state.collabVersion = 0;
  state.streamReady = false;
  renderPresence();
  renderRemoteCursors();
  const stream = new EventSource(`/api/file/stream?path=${encodeURIComponent(path)}&clientId=${encodeURIComponent(state.clientId)}`);
  state.stream = stream;
  stream.addEventListener("snapshot", (event) => {
    state.streamReady = true;
    clearTimeout(state.collabFallbackTimer);
    handleCollabMessage(JSON.parse(event.data));
  });
  stream.addEventListener("update", (event) => handleCollabMessage(JSON.parse(event.data)));
  stream.addEventListener("presence", (event) => handleCollabMessage(JSON.parse(event.data)));
  stream.addEventListener("draft", (event) => handleCollabMessage(JSON.parse(event.data)));
  stream.addEventListener("leave", (event) => handleCollabMessage(JSON.parse(event.data)));
  stream.onopen = () => sendCollabPresence().catch(() => {});
  state.presenceHeartbeatTimer = setInterval(() => sendCollabPresence().catch(() => {}), 15000);
  state.collabFallbackTimer = setTimeout(() => {
    if (!state.streamReady) startCollabPolling();
  }, 1500);
  stream.onerror = () => {
    if (state.stream === stream) {
      startCollabPolling();
      showToast("Live collaboration disconnected. Reopen the document to reconnect.");
    }
  };
  state.remoteCleanupTimer = setInterval(pruneRemoteClients, 5000);
}

function disconnectCollab() {
  clearTimeout(state.collabTimer);
  clearTimeout(state.presenceTimer);
  clearTimeout(state.collabFallbackTimer);
  clearTimeout(state.collabPollTimer);
  state.collabTimer = null;
  state.presenceTimer = null;
  state.collabFallbackTimer = null;
  state.collabPollTimer = null;
  state.collabPolling = false;
  state.streamReady = false;
  clearInterval(state.presenceHeartbeatTimer);
  state.presenceHeartbeatTimer = null;
  clearInterval(state.remoteCleanupTimer);
  state.remoteCleanupTimer = null;
  if (state.stream) {
    state.stream.close();
    state.stream = null;
  }
  state.remoteClients.clear();
  renderPresence();
  renderRemoteCursors();
}

function handleCollabMessage(message) {
  updateCollabVersion(message);
  switch (message.type) {
    case "snapshot":
    case "update":
      handleRemoteDocument(message);
      break;
    case "presence":
      handleRemotePresence(message);
      break;
    case "draft":
      handleRemoteDraft(message);
      break;
    case "leave":
      handleRemoteLeave(message);
      break;
    default:
      break;
  }
}

function updateCollabVersion(message) {
  const version = Number(message?.version) || 0;
  if (version > state.collabVersion) {
    state.collabVersion = version;
  }
}

function startCollabPolling() {
  if (state.collabPolling || !state.file) return;
  state.collabPolling = true;
  pollCollab();
}

async function pollCollab() {
  if (!state.collabPolling || !state.file) return;
  try {
    const data = await api(`/api/file/collab?path=${encodeURIComponent(state.file.path)}&clientId=${encodeURIComponent(state.clientId)}&since=${state.collabVersion}`);
    (data.events || []).forEach((message) => handleCollabMessage(message));
    const version = Number(data.version) || 0;
    if (version > state.collabVersion) {
      state.collabVersion = version;
    }
  } catch (_) {
    // Keep polling; transient tunnel failures should not permanently break collaboration.
  }
  if (state.collabPolling) {
    state.collabPollTimer = setTimeout(() => pollCollab(), 1000);
  }
}

function handleRemoteDocument(message) {
  if (!state.file || message.path !== state.file.path) return;
  if (message.type !== "snapshot" && message.clientId && message.clientId === state.clientId) return;
  if (message.modified) {
    state.file.modified = message.modified;
  }
  if (typeof message.content !== "string" || message.content === state.content) return;

  if (state.dirty) {
    const result = applyRemoteMarkdown(message.content);
    if (result.skipped) {
      state.remotePending = message;
      const who = displayUser(message.user);
      showToast(`${who} saved changes in a block you are editing.`);
    } else {
      state.remotePending = null;
      setSaveState("Editing with live updates", "dirty");
    }
    return;
  }

  state.content = message.content;
  state.dirty = false;
  state.localDirtyBlocks.clear();
  renderMarkdownDocument(message.content);
  setSaveState(`Updated by ${displayUser(message.user)}`, "saved");
  if (state.historyOpen) {
    refreshHistory().catch(() => {});
  }
}

function handleRemoteDraft(message) {
  if (!state.file || message.path !== state.file.path) return;
  if (message.clientId && message.clientId === state.clientId) return;
  rememberRemoteClient(message);
  if (typeof message.content === "string" && message.content !== state.content) {
    applyRemoteMarkdown(message.content);
    setSaveState(state.dirty ? "Editing with live updates" : `Live with ${displayUser(message.user)}`, state.dirty ? "dirty" : "saved");
  }
  renderRemoteCursors();
}

function handleRemotePresence(message) {
  if (!state.file || message.path !== state.file.path) return;
  if (message.clientId && message.clientId === state.clientId) return;
  rememberRemoteClient(message);
  renderRemoteCursors();
}

function handleRemoteLeave(message) {
  if (!message.clientId) return;
  state.remoteClients.delete(message.clientId);
  renderPresence();
  renderRemoteCursors();
}

function applyRemoteMarkdown(markdown) {
  const remoteBlocks = parseMarkdownBlocks(markdown || "");
  if (!remoteBlocks.length) {
    remoteBlocks.push({ type: "p", html: "" });
  }
  const localBlocks = [...els.editor.querySelectorAll(".block")];
  let changed = false;
  let skipped = 0;

  state.applying = true;
  remoteBlocks.forEach((remoteBlock, index) => {
    const block = localBlocks[index];
    if (!block) {
      const next = els.editor.querySelectorAll(".block")[index] || null;
      els.editor.insertBefore(createBlock(remoteBlock.type, remoteBlock.html, remoteBlock.checked), next);
      changed = true;
      return;
    }
    if (isProtectedBlock(block)) {
      skipped++;
      return;
    }
    changed = applyRemoteBlock(block, remoteBlock) || changed;
  });

  [...els.editor.querySelectorAll(".block")].slice(remoteBlocks.length).reverse().forEach((block) => {
    if (isProtectedBlock(block)) {
      skipped++;
      return;
    }
    block.remove();
    changed = true;
  });
  state.applying = false;

  if (changed) {
    state.content = serializeDocument();
  }
  renderRemoteCursors();
  return { changed, skipped };
}

function applyRemoteBlock(block, remoteBlock) {
  const type = remoteBlock.type || "p";
  const html = remoteBlock.html || "";
  const checked = !!remoteBlock.checked;
  let changed = false;
  if (block.dataset.type !== type) {
    setBlockType(block, type);
    changed = true;
  }
  if (block.innerHTML !== html) {
    block.innerHTML = html;
    changed = true;
  }
  if (block.classList.contains("checked") !== checked) {
    block.classList.toggle("checked", checked);
    changed = true;
  }
  return changed;
}

function isProtectedBlock(block) {
  if (!state.dirty) return false;
  return state.localDirtyBlocks.has(block) || block === state.activeBlock || block === currentSelectionBlock();
}

function scheduleCollabDraft() {
  if (!state.file || !state.stream) return;
  clearTimeout(state.collabTimer);
  state.collabTimer = setTimeout(() => sendCollabDraft().catch(() => {}), 120);
}

function scheduleCollabPresence() {
  if (!state.file || !state.stream) return;
  clearTimeout(state.presenceTimer);
  state.presenceTimer = setTimeout(() => sendCollabPresence().catch(() => {}), 120);
}

async function sendCollabDraft() {
  await sendCollabEvent(true);
}

async function sendCollabPresence() {
  await sendCollabEvent(false);
}

async function sendCollabEvent(includeContent) {
  if (!state.file) return;
  const cursor = currentCursor();
  const payload = {
    path: state.file.path,
    clientId: state.clientId,
  };
  if (includeContent) {
    payload.content = serializeDocument();
  }
  if (cursor) {
    payload.cursor = cursor;
  }
  await api("/api/file/collab", {
    method: "POST",
    body: JSON.stringify(payload),
  });
}

function rememberRemoteClient(message) {
  if (!message.clientId) return;
  const current = state.remoteClients.get(message.clientId) || {};
  state.remoteClients.set(message.clientId, {
    user: message.user,
    cursor: message.cursor || current.cursor,
    color: current.color || colorForClient(message.clientId),
    lastSeen: Date.now(),
  });
  renderPresence();
}

function pruneRemoteClients() {
  const staleBefore = Date.now() - 30000;
  let changed = false;
  state.remoteClients.forEach((client, clientId) => {
    if (client.lastSeen < staleBefore) {
      state.remoteClients.delete(clientId);
      changed = true;
    }
  });
  if (changed) {
    renderPresence();
    renderRemoteCursors();
  }
}

function renderPresence() {
  if (!els.collabPresence) return;
  els.collabPresence.innerHTML = "";
  state.remoteClients.forEach((client) => {
    const badge = document.createElement("span");
    badge.className = "collab-avatar";
    badge.style.setProperty("--collab-color", client.color);
    badge.title = displayUser(client.user);
    badge.textContent = initialsForUser(client.user);
    els.collabPresence.append(badge);
  });
}

function renderRemoteCursors() {
  if (!els.remoteCursors || !els.pageCard) return;
  els.remoteCursors.innerHTML = "";
  const pageRect = els.pageCard.getBoundingClientRect();
  state.remoteClients.forEach((client) => {
    if (!client.cursor) return;
    const position = remoteCursorPosition(client.cursor, pageRect);
    if (!position) return;
    const cursor = document.createElement("div");
    cursor.className = "remote-cursor";
    cursor.style.setProperty("--collab-color", client.color);
    cursor.style.left = `${position.left}px`;
    cursor.style.top = `${position.top}px`;
    cursor.style.height = `${position.height}px`;
    const label = document.createElement("span");
    label.textContent = displayUser(client.user);
    cursor.append(label);
    els.remoteCursors.append(cursor);
  });
}

function remoteCursorPosition(cursor, pageRect) {
  const blocks = [...els.editor.querySelectorAll(".block")];
  const block = blocks[cursor.blockIndex];
  if (!block) return null;
  const range = rangeForTextOffset(block, cursor.offset);
  const rect = firstCursorRect(range) || block.getBoundingClientRect();
  if (!rect) return null;
  const lineHeight = parseFloat(getComputedStyle(block).lineHeight) || 18;
  return {
    left: rect.left - pageRect.left,
    top: rect.top - pageRect.top,
    height: Math.max(rect.height || lineHeight, 16),
  };
}

function firstCursorRect(range) {
  if (!range) return null;
  const rects = range.getClientRects();
  for (const rect of rects) {
    if (rect.height) return rect;
  }
  const rect = range.getBoundingClientRect();
  return rect.height ? rect : null;
}

function currentCursor() {
  const selection = window.getSelection();
  if (!selection || selection.rangeCount === 0) return null;
  const block = closestBlock(selection.focusNode);
  if (!block) return null;
  const blocks = [...els.editor.querySelectorAll(".block")];
  const blockIndex = blocks.indexOf(block);
  if (blockIndex < 0) return null;
  return {
    blockIndex,
    offset: caretOffsetInBlock(block, selection.focusNode, selection.focusOffset),
  };
}

function currentSelectionBlock() {
  const selection = window.getSelection();
  if (!selection || selection.rangeCount === 0) return null;
  return closestBlock(selection.focusNode);
}

function closestBlock(node) {
  if (!node) return null;
  const element = node.nodeType === Node.ELEMENT_NODE ? node : node.parentElement;
  const block = element?.closest?.(".block");
  return block && els.editor.contains(block) ? block : null;
}

function caretOffsetInBlock(block, node, offset) {
  try {
    const range = document.createRange();
    range.selectNodeContents(block);
    range.setEnd(node, offset);
    return range.toString().replace(/\u00a0/g, " ").length;
  } catch (_) {
    return normalizedText(block).length;
  }
}

function rangeForTextOffset(block, offset) {
  const range = document.createRange();
  let remaining = Math.max(0, Number(offset) || 0);
  if (remaining === 0) {
    range.selectNodeContents(block);
    range.collapse(true);
    return range;
  }
  const walker = document.createTreeWalker(block, NodeFilter.SHOW_TEXT);
  let last = null;
  while (walker.nextNode()) {
    const node = walker.currentNode;
    const length = node.nodeValue.length;
    last = node;
    if (remaining <= length) {
      range.setStart(node, remaining);
      range.collapse(true);
      return range;
    }
    remaining -= length;
  }
  if (last) {
    range.setStart(last, last.nodeValue.length);
    range.collapse(true);
  } else {
    range.selectNodeContents(block);
    range.collapse(false);
  }
  return range;
}

function initialsForUser(user) {
  const name = displayUser(user).trim();
  const parts = name.split(/\s+/).filter(Boolean);
  if (!parts.length) return "?";
  return parts.slice(0, 2).map((part) => part[0]).join("").toUpperCase();
}

function colorForClient(clientId) {
  const palette = ["#7b245f", "#d93025", "#188038", "#9334e6", "#f29900", "#00acc1", "#e52592", "#5f6368"];
  let hash = 0;
  for (const char of clientId) {
    hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  }
  return palette[hash % palette.length];
}

function displayUser(user) {
  if (!user) return "Someone";
  return user.name || user.email || "Someone";
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
  const block = createBlock(type, html, checked);
  if (after) {
    after.insertAdjacentElement("afterend", block);
  } else {
    els.editor.append(block);
  }
  return block;
}

function createBlock(type, html = "", checked = false) {
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
  return block;
}

function bindBlock(block) {
  block.addEventListener("focus", () => {
    state.activeBlock = block;
    scheduleCollabPresence();
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
  const block = currentSelectionBlock() || state.activeBlock;
  if (block) {
    state.localDirtyBlocks.add(block);
  }
  state.content = serializeDocument();
  state.dirty = true;
  setSaveState("Editing", "dirty");
  scheduleCollabDraft();
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
  if (state.remotePending) {
    const overwrite = confirm("Someone saved changes while you were editing. Save your version and overwrite the remote changes?");
    if (!overwrite) {
      setSaveState("Remote changes pending", "dirty");
      return;
    }
    state.remotePending = null;
  }
  const content = serializeDocument();
  if (!state.dirty && content === state.content) {
    setSaveState("Saved", "saved");
    return;
  }
  state.saving = true;
  setSaveState("Saving", "dirty");
  let result;
  try {
    result = await api("/api/file", {
      method: "PUT",
      body: JSON.stringify({ path: state.file.path, content, clientId: state.clientId, baseModified: state.file.modified || "" }),
    });
  } catch (error) {
    if (error.status !== 409) {
      state.saving = false;
      throw error;
    }
    const overwrite = confirm("This file changed on the server since you loaded it. Save your version and overwrite it? (The other version stays in history.)");
    if (!overwrite) {
      state.saving = false;
      setSaveState("Server changes pending", "dirty");
      return;
    }
    result = await api("/api/file", {
      method: "PUT",
      body: JSON.stringify({ path: state.file.path, content, clientId: state.clientId, force: true }),
    });
  }
  state.file.modified = result.modified;
  state.file.size = result.size;
  state.content = content;
  state.dirty = false;
  state.saving = false;
  state.localDirtyBlocks.clear();
  setSaveState("Saved", "saved");
  loadDirectory(state.directory).catch(() => {});
  if (state.historyOpen) {
    refreshHistory().catch(() => {});
  }
}

function toggleHistoryPanel() {
  if (state.historyOpen) {
    closeHistoryPanel();
  } else {
    openHistoryPanel();
  }
}

function openHistoryPanel() {
  if (state.view !== "editor" || !state.file) return;
  closeFileMenu();
  state.historyOpen = true;
  state.historySelected = null;
  els.historyPanel.hidden = false;
  els.historyToggle.classList.add("active");
  els.historyToggle.setAttribute("aria-expanded", "true");
  renderHistoryPreview(null);
  refreshHistory().catch((error) => showError(error.message));
}

function closeHistoryPanel() {
  if (!state.historyOpen) return;
  state.historyOpen = false;
  state.historySelected = null;
  els.historyPanel.hidden = true;
  els.historyToggle.classList.remove("active");
  els.historyToggle.setAttribute("aria-expanded", "false");
}

async function refreshHistory() {
  if (!state.file) return;
  const data = await api(`/api/file/history?path=${encodeURIComponent(state.file.path)}`);
  state.historyNodes = data.nodes || [];
  if (state.historySelected && !state.historyNodes.some((node) => node.id === state.historySelected)) {
    state.historySelected = null;
    renderHistoryPreview(null);
  }
  if (data.enabled === false) {
    els.historyEmpty.textContent = "Save history is disabled on this server.";
  } else {
    els.historyEmpty.textContent = "No saved versions yet. Versions appear after the first save.";
  }
  renderHistoryGraph();
  const selected = selectedHistoryNode();
  if (selected) {
    renderHistoryPreviewHeaderOnly(selected);
  }
}

// Lays out the single-parent commit tree: rows are saves (newest first),
// lanes are branches. A lane "waits" for the parent of the last node it
// placed; when several lanes wait for the same commit, the branches join.
function layoutHistoryTree(nodes) {
  const lanes = [];
  const rows = [];
  const edges = [];
  nodes.forEach((node, row) => {
    let lane = -1;
    for (let i = 0; i < lanes.length; i++) {
      if (lanes[i] && lanes[i].sha === node.id) {
        if (lane === -1) lane = i;
        edges.push({ fromRow: lanes[i].childRow, fromLane: i, toRow: row, toLane: lane });
        if (i !== lane) lanes[i] = null;
      }
    }
    if (lane === -1) {
      lane = lanes.findIndex((slot) => slot === null);
      if (lane === -1) lane = lanes.length;
    }
    lanes[lane] = node.parent ? { sha: node.parent, childRow: row, childLane: lane } : null;
    rows.push({ node, row, lane });
  });
  const laneCount = Math.max(1, ...rows.map((entry) => entry.lane + 1));
  return { rows, edges, laneCount };
}

const HISTORY_ROW_HEIGHT = 46;
const HISTORY_LANE_WIDTH = 16;
const HISTORY_GRAPH_PAD = 14;

function historyNodeX(lane) {
  return HISTORY_GRAPH_PAD + lane * HISTORY_LANE_WIDTH;
}

function historyNodeY(row) {
  return row * HISTORY_ROW_HEIGHT + HISTORY_ROW_HEIGHT / 2;
}

function renderHistoryGraph() {
  els.historyGraph.innerHTML = "";
  const nodes = state.historyNodes;
  els.historyEmpty.hidden = nodes.length > 0;
  if (!nodes.length) return;

  const { rows, edges, laneCount } = layoutHistoryTree(nodes);
  const graphWidth = HISTORY_GRAPH_PAD * 2 + (laneCount - 1) * HISTORY_LANE_WIDTH;
  const height = nodes.length * HISTORY_ROW_HEIGHT;

  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("class", "history-svg");
  svg.setAttribute("width", graphWidth);
  svg.setAttribute("height", height);

  edges.forEach((edge) => {
    const x1 = historyNodeX(edge.fromLane);
    const y1 = historyNodeY(edge.fromRow);
    const x2 = historyNodeX(edge.toLane);
    const y2 = historyNodeY(edge.toRow);
    const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
    const bend = Math.min(HISTORY_ROW_HEIGHT / 2, (y2 - y1) / 2);
    path.setAttribute("d", x1 === x2
      ? `M ${x1} ${y1} L ${x2} ${y2}`
      : `M ${x1} ${y1} L ${x1} ${y2 - bend} Q ${x1} ${y2} ${x1 + Math.sign(x2 - x1) * Math.min(Math.abs(x2 - x1), 12)} ${y2} L ${x2} ${y2}`);
    path.setAttribute("class", "history-edge");
    svg.append(path);
  });

  rows.forEach(({ node, row, lane }) => {
    const circle = document.createElementNS("http://www.w3.org/2000/svg", "circle");
    circle.setAttribute("cx", historyNodeX(lane));
    circle.setAttribute("cy", historyNodeY(row));
    circle.setAttribute("r", node.current ? 6 : 4.5);
    circle.setAttribute("class", `history-node${node.current ? " current" : ""}${node.id === state.historySelected ? " selected" : ""}`);
    svg.append(circle);
  });

  els.historyGraph.append(svg);

  rows.forEach(({ node, row }) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = `history-row${node.id === state.historySelected ? " selected" : ""}${node.current ? " current" : ""}`;
    button.style.top = `${row * HISTORY_ROW_HEIGHT}px`;
    button.style.left = `${graphWidth}px`;
    button.title = formatDate(node.time);
    const badge = node.current ? `<span class="history-badge">Current</span>` : "";
    const title = node.name
      ? `<span class="history-row-name">${escapeHTML(node.name)}</span>`
      : escapeHTML(formatRelative(node.time));
    const stats = node.additions || node.deletions
      ? ` · <span class="diff-stat-add">+${node.additions}</span> <span class="diff-stat-del">−${node.deletions}</span>`
      : "";
    const when = node.name ? `${escapeHTML(formatRelative(node.time))} · ` : "";
    button.innerHTML = `
      <span class="history-row-time">${title}${badge}</span>
      <span class="history-row-author">${when}${escapeHTML(node.author || "")}${stats}</span>
    `;
    button.addEventListener("click", () => selectHistoryNode(node).catch((error) => showError(error.message)));
    els.historyGraph.append(button);
  });
  els.historyGraph.style.height = `${height}px`;
}

async function selectHistoryNode(node) {
  state.historySelected = node.id;
  renderHistoryGraph();
  renderHistoryPreview(node);
  await loadHistoryTab(node);
}

function setHistoryTab(tab) {
  state.historyTab = tab;
  const node = selectedHistoryNode();
  if (!node) return;
  renderHistoryPreview(node);
  loadHistoryTab(node).catch((error) => showError(error.message));
}

function selectedHistoryNode() {
  return state.historyNodes.find((node) => node.id === state.historySelected) || null;
}

async function loadHistoryTab(node) {
  const tab = state.historyTab;
  if (tab === "document") {
    const data = await api(`/api/file/history/content?path=${encodeURIComponent(state.file.path)}&id=${encodeURIComponent(node.id)}`);
    if (state.historySelected !== node.id || state.historyTab !== tab) return;
    renderHistoryDocument(data.content || "");
  } else {
    const data = await api(`/api/file/history/diff?path=${encodeURIComponent(state.file.path)}&id=${encodeURIComponent(node.id)}`);
    if (state.historySelected !== node.id || state.historyTab !== tab) return;
    renderHistoryDiff(data.diff || "");
  }
}

function renderHistoryPreview(node) {
  if (!node) {
    els.historyPreview.hidden = true;
    els.historyPreviewBody.innerHTML = "";
    return;
  }
  els.historyPreview.hidden = false;
  els.historyTabChanges.classList.toggle("active", state.historyTab === "changes");
  els.historyTabDocument.classList.toggle("active", state.historyTab === "document");
  els.historyRestore.disabled = node.current;
  els.historyRestore.textContent = node.current ? "Current version" : "Restore";
  els.historyName.textContent = node.name ? "Rename" : "Name";
  els.historyPreviewBody.innerHTML = "";
}

function renderHistoryDocument(content) {
  els.historyPreviewBody.innerHTML = "";
  parseMarkdownBlocks(content || "").forEach((block) => {
    const el = document.createElement("div");
    el.className = `block block-${block.type}`;
    if (block.checked) el.classList.add("checked");
    el.innerHTML = block.html;
    els.historyPreviewBody.append(el);
  });
  if (!els.historyPreviewBody.children.length) {
    els.historyPreviewBody.textContent = "Empty document.";
  }
}

function renderHistoryDiff(diff) {
  els.historyPreviewBody.innerHTML = "";
  const container = document.createElement("div");
  container.className = "history-diff";
  let lines = 0;
  for (const line of (diff || "").split("\n")) {
    if (/^(diff --git|index |--- |\+\+\+ |new file|deleted file|old mode|new mode|\\ No newline)/.test(line)) continue;
    const el = document.createElement("div");
    if (line.startsWith("@@")) {
      el.className = "diff-hunk";
      el.textContent = "···";
    } else if (line.startsWith("+")) {
      el.className = "diff-add";
      el.textContent = line.slice(1) || " ";
    } else if (line.startsWith("-")) {
      el.className = "diff-del";
      el.textContent = line.slice(1) || " ";
    } else {
      el.className = "diff-ctx";
      el.textContent = line.slice(1) || " ";
    }
    container.append(el);
    lines++;
  }
  if (!lines) {
    container.textContent = "No changes in this save.";
  }
  els.historyPreviewBody.append(container);
}

async function nameSelectedVersion() {
  const node = selectedHistoryNode();
  if (!node || !state.file) return;
  const name = prompt("Name this version (empty to remove the name)", node.name || "");
  if (name === null) return;
  await api("/api/file/history/label", {
    method: "POST",
    body: JSON.stringify({ path: state.file.path, id: node.id, name: name.trim() }),
  });
  await refreshHistory();
  const updated = selectedHistoryNode();
  if (updated) renderHistoryPreviewHeaderOnly(updated);
}

function renderHistoryPreviewHeaderOnly(node) {
  els.historyRestore.disabled = node.current;
  els.historyRestore.textContent = node.current ? "Current version" : "Restore";
  els.historyName.textContent = node.name ? "Rename" : "Name";
}

async function restoreSelectedVersion() {
  if (!state.file || !state.historySelected) return;
  if (state.dirty) {
    const proceed = confirm("You have unsaved edits. Restoring will replace them with the selected version. Continue?");
    if (!proceed) return;
  }
  try {
    const result = await api("/api/file/restore", {
      method: "POST",
      body: JSON.stringify({ path: state.file.path, id: state.historySelected, clientId: state.clientId }),
    });
    state.content = result.content || "";
    state.dirty = false;
    state.localDirtyBlocks.clear();
    state.remotePending = null;
    if (result.modified) {
      state.file.modified = result.modified;
    }
    renderMarkdownDocument(state.content);
    setSaveState("Restored", "saved");
    showToast("Restored selected version. New edits will branch from it.");
    await refreshHistory();
  } catch (error) {
    showError(error.message);
  }
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
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      ...(options.headers || {}),
    },
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    if (response.status === 401) {
      if (window.Shoo) window.Shoo.clearIdentity();
      state.user = null;
      showAuthView();
    }
    const error = new Error(data.error || `${response.status} ${response.statusText}`);
    error.status = response.status;
    throw error;
  }
  return data;
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

function formatRelative(value) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const seconds = (Date.now() - date.getTime()) / 1000;
  if (seconds < 45) return "just now";
  if (seconds < 3600) return `${Math.max(1, Math.round(seconds / 60))} min ago`;
  if (seconds < 86400) return `${Math.round(seconds / 3600)} h ago`;
  if (seconds < 7 * 86400) return `${Math.round(seconds / 86400)} d ago`;
  return formatDate(value);
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
