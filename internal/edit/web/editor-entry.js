import {basicSetup} from "codemirror";
import {markdown} from "@codemirror/lang-markdown";
import {indentUnit} from "@codemirror/language";
import {indentWithTab} from "@codemirror/commands";
import {Compartment, EditorState, Prec} from "@codemirror/state";
import {EditorView, keymap} from "@codemirror/view";
import {MergeView} from "@codemirror/merge";

const $ = selector => document.querySelector(selector);
const fields = [...document.querySelectorAll("[data-field]")];
const sectionFields = new Set(["title", "publish", "description", "order", "banner", "bannerAlt"]);
const state = {
  path: new URLSearchParams(location.search).get("path") || "",
  kind: "article", mode: "visual", source: "", baselineSource: "", hash: "",
  bodyBaseline: "", formBaseline: {}, sourceChanged: false,
  filePath: "", fileHash: "", fileKind: "", fileSourceKind: "",
  fileDialogMode: "", dirty: false, busy: false, loadToken: 0,
  draftTimer: 0, diffView: null, previewURL: "", fileEditable: false, syncScroll: false, syncingScroll: false, indent: 2, keepPreview: true, previewOpen: false, suppressPreview: false, catalogSources: [], collapsedFolders: new Set(), collapsedSourceFolders: new Set(),
};
const editable = new Compartment();
let editor;
let suppressChanges = false;

function status(message) { $("#status").textContent = message; }
function saveState(value, label = "") {
  const target = $("#save-state");
  if (target) { target.dataset.state = value; target.textContent = label || value; }
  const footer = $("#draft-indicator");
  if (footer) footer.textContent = label || value;
}
function updateEditorStats() {
  if (!editor) return;
  const value = text();
  $("#line-count").textContent = `Lines: ${value ? value.split(/\r?\n/).length : 0}`;
  $("#word-count").textContent = `Words: ${value.trim() ? value.trim().split(/\s+/u).length : 0}`;
  $("#file-info").textContent = state.path || "No file";
}
function dirty(value = true) {
  state.dirty = value;
  document.title = value ? "* Obsite Editor" : "Obsite Editor";
  saveState(value ? "unsaved" : "saved", value ? "Unsaved" : "Saved");
  if (value) scheduleDraft();
}
function scheduleDraft() {
  clearTimeout(state.draftTimer);
  if (!state.path || !window.indexedDB) return;
  saveState("unsaved", "Unsaved");
  state.draftTimer = setTimeout(() => saveDraft().catch(() => status("Local draft could not be saved; check browser storage.")), 1500);
}
function busy(value) {
  state.busy = value;
  document.querySelectorAll(".header-actions button, #source, #refresh, #file-refresh, .file-actions button, #metadata input, #metadata select").forEach(input => { input.disabled = value; });
  $("#delete").disabled = value || state.kind !== "article";
  updateFileActions();
  editor.dispatch({effects: editable.reconfigure(EditorView.editable.of(!value))});
}
function text() { return editor.state.doc.toString(); }
function setText(value) {
  suppressChanges = true;
  // A new state prevents undo from recovering a different page's content or YAML.
  editor.setState(EditorState.create({doc: value, extensions: editorExtensions()}));
  suppressChanges = false;
  updateEditorStats();
}

let draftDB;
function openDraftDB() {
  if (draftDB || !window.indexedDB) return draftDB;
  draftDB = new Promise((resolve, reject) => {
    const request = indexedDB.open("obsite-editor", 1);
    request.onupgradeneeded = () => request.result.createObjectStore("drafts", {keyPath: "key"});
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
  return draftDB;
}
async function draftRecord(key) {
  const db = await openDraftDB();
  if (!db) return null;
  return new Promise((resolve, reject) => {
    const request = db.transaction("drafts", "readonly").objectStore("drafts").get(key);
    request.onsuccess = () => resolve(request.result || null);
    request.onerror = () => reject(request.error);
  });
}
async function saveDraft() {
  if (!state.path || !state.dirty) return;
  const db = await openDraftDB();
  if (!db) return;
  const candidate = {mode: state.mode, source: state.mode === "source" ? state.source : "", body: state.mode === "source" ? "" : text(), fields: state.mode === "source" ? null : formValues()};
  await new Promise((resolve, reject) => {
    const transaction = db.transaction("drafts", "readwrite");
    transaction.objectStore("drafts").put({key: `${state.path}:${state.hash}`, path: state.path, hash: state.hash, candidate, savedAt: Date.now()});
    transaction.oncomplete = resolve;
    transaction.onerror = () => reject(transaction.error);
  });
  saveState("draft", "Draft saved locally");
}
async function clearDraftKey(key) {
  const db = await openDraftDB();
  if (!db || !key) return;
  await new Promise(resolve => {
    const transaction = db.transaction("drafts", "readwrite");
    transaction.objectStore("drafts").delete(key);
    transaction.oncomplete = resolve;
    transaction.onerror = resolve;
  });
}
async function clearDraft(path = state.path) {
  const db = await openDraftDB();
  if (!db || !path) return;
  await new Promise(resolve => {
    const transaction = db.transaction("drafts", "readwrite");
    const drafts = transaction.objectStore("drafts");
    const request = drafts.getAll();
    request.onsuccess = () => request.result.filter(record => record.path === path).forEach(record => drafts.delete(record.key));
    transaction.oncomplete = resolve;
    transaction.onerror = resolve;
  });
}
async function offerDraft(path, hash) {
  if (!window.indexedDB) return;
  const exact = await draftRecord(`${path}:${hash}`);
  let record = exact;
  if (!record) {
    const db = await openDraftDB();
    if (!db) return;
    record = await new Promise((resolve, reject) => {
      const request = db.transaction("drafts", "readonly").objectStore("drafts").getAll();
      request.onsuccess = () => resolve(request.result.filter(item => item.path === path).sort((a, b) => b.savedAt - a.savedAt)[0] || null);
      request.onerror = () => reject(request.error);
    });
  }
  if (!record || !record.candidate) return;
  if (confirm(`Restore the unsaved local draft for ${path}?`)) {
    state.mode = record.candidate.mode || "source";
    if (state.mode === "source") {
      state.source = record.candidate.source || state.source;
      setText(state.source);
    } else {
      showForm(record.candidate.fields || formValues());
      setText(record.candidate.body || "");
      state.bodyBaseline = "";
    }
    updateMode();
    dirty(true);
    status("Local draft restored; save to publish it.");
  } else {
    await clearDraftKey(record.key);
  }
}
function applyIndentPreference() {
  if (!editor) return;
  const spaces = Math.max(1, Math.min(8, Number(state.indent) || 2));
  editor.dispatch({effects: indentation.reconfigure(indentExtensions(spaces))});
}
function loadPreferences() {
  try {
    const preferences = JSON.parse(localStorage.getItem("obsite-editor-preferences") || "{}");
    state.indent = Math.max(1, Math.min(8, Number(preferences.indent) || 2));
    state.keepPreview = preferences.keepPreview !== false;
    document.documentElement.dataset.theme = preferences.theme && preferences.theme !== "system" ? preferences.theme : "";
    document.documentElement.style.colorScheme = preferences.theme === "dark" ? "dark" : preferences.theme === "light" ? "light" : "";
    $("#indent-setting").textContent = `Indent: ${state.indent} spaces`;
    $("#preference-theme").value = preferences.theme || "system";
    $("#preference-indent").value = state.indent;
    $("#preference-preview").checked = state.keepPreview;
    applyIndentPreference();
  } catch { /* browser preferences are best effort and never enter the vault */ }
}
function storePreferences() {
  const preferences = {theme: $("#preference-theme").value, indent: Number($("#preference-indent").value) || 2, keepPreview: $("#preference-preview").checked};
  state.indent = Math.max(1, Math.min(8, preferences.indent));
  state.keepPreview = preferences.keepPreview;
  localStorage.setItem("obsite-editor-preferences", JSON.stringify({...preferences, indent: state.indent}));
  document.documentElement.dataset.theme = preferences.theme === "system" ? "" : preferences.theme;
  document.documentElement.style.colorScheme = preferences.theme === "dark" ? "dark" : preferences.theme === "light" ? "light" : "";
  $("#indent-setting").textContent = `Indent: ${state.indent} spaces`;
  applyIndentPreference();
}function formValues() {
  const result = {};
  for (const input of fields) {
    const name = input.dataset.field;
    if (state.kind === "section" && !sectionFields.has(name)) continue;
    if (name === "publish") result[name] = input.checked;
    else if (name === "tags" || name === "aliases") result[name] = input.value.split(/[,\n]/).map(value => value.trim()).filter(Boolean);
    else if (name === "order") result[name] = input.value === "" ? null : Number(input.value);
    else result[name] = input.value;
  }
  return result;
}
function showForm(metadata) {
  const advanced = document.querySelector(".metadata-advanced");
  if (advanced) advanced.hidden = state.kind === "section";
  for (const input of fields) {
    const name = input.dataset.field;
    const value = metadata[name];
    if (name === "publish") input.checked = value === true;
    else if (Array.isArray(value)) input.value = value.join(", ");
    else input.value = value == null ? "" : String(value);
    input.closest("label").hidden = state.kind === "section" && !sectionFields.has(name);
  }
  state.formBaseline = formValues();
  $("#metadata-title").textContent = metadata.title || state.path || "Current source";
  const published = metadata.publish === true;
  $("#publish-badge").textContent = published ? "Published" : "Draft";
  $("#publish-badge").className = `state-badge ${published ? "published" : "draft"}`;
  const labels = {doc: "Document", post: "Post", page: "Page"};
  $("#type-badge").textContent = labels[metadata.type] || (state.kind === "section" ? "Section" : "Document");
}
function changes() {
  const current = formValues();
  return Object.keys(current).filter(field => JSON.stringify(current[field]) !== JSON.stringify(state.formBaseline[field]));
}
function diagnostics(items = [], message = "") {
  items = items || [];
  const root = $("#diagnostics");
  root.replaceChildren();
  root.hidden = !message && !items.length;
  if (message) { const p = document.createElement("p"); p.textContent = message; root.append(p); }
  const list = document.createElement("ul");
  for (const item of items) {
    const li = document.createElement("li");
    const location = item.Location || {};
    li.textContent = `${location.Path || ""}${location.Line ? `:${location.Line}` : ""} ${item.Message}`;
    list.append(li);
  }
  root.append(list);
}
async function responseJSON(response) {
  const body = await response.text();
  const data = response.headers.get("Content-Type")?.includes("application/json") ? JSON.parse(body) : {};
  if (!response.ok) {
    const error = new Error(data.error || body || `Request failed (${response.status})`);
    error.diagnostics = data.diagnostics || [];
    throw error;
  }
  return data;
}
async function mutation(method, url, body, headers = {}) {
  const session = await responseJSON(await fetch("/_obsite/csrf"));
  headers = {...headers, "X-Obsite-CSRF": session.csrf};
  if (body && !(body instanceof FormData) && !(body instanceof URLSearchParams) && typeof body !== "string") {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(body);
  }
  return responseJSON(await fetch(url, {method, headers, body}));
}
function renderSourceTree() {
  const tree = $("#source-tree");
  tree.replaceChildren();
  const query = ($("#document-search")?.value || "").trim().toLowerCase();
  const sources = state.catalogSources.filter(source => !query || `${source.relPath} ${source.title} ${source.effectivePublish ? "published" : "draft"}`.toLowerCase().includes(query));
  const parentPath = value => value.includes("/") ? value.slice(0, value.lastIndexOf("/")) : "";
  const folders = new Set([""]);
  for (const source of sources) {
    const segments = source.relPath.split("/");
    for (let index = 1; index < segments.length; index++) folders.add(segments.slice(0, index).join("/"));
  }
  const appendSource = (source, depth) => {
    const item = document.createElement("button");
    item.type = "button";
    item.className = "source-tree-entry";
    item.dataset.path = source.relPath;
    item.setAttribute("role", "treeitem");
    item.setAttribute("aria-selected", String(source.relPath === state.path));
    item.style.paddingLeft = `${0.35 + depth * 0.8}rem`;
    const icon = document.createElement("span");
    icon.className = "source-tree-icon";
    icon.innerHTML = `<svg aria-hidden="true"><use href="#icon-${source.kind === "section" ? "folder" : "code"}"></use></svg>`;
    const label = document.createElement("span");
    label.className = "source-tree-label";
    label.textContent = source.title || source.relPath;
    const meta = document.createElement("small");
    meta.className = "source-tree-meta";
    meta.textContent = `${source.kind === "section" ? "Section" : "Article"} · ${source.effectivePublish ? "Published" : "Draft"}`;
    item.append(icon, label, meta);
    item.title = source.relPath;
    item.onclick = () => { if (!confirmDiscard()) return; $("#source").value = source.relPath; loadSource(source.relPath).catch(error => status(`Load failed: ${error.message}`)); };
    tree.append(item);
  };
  const appendFolder = (folder, depth) => {
    if (folder) {
      const item = document.createElement("button");
      item.type = "button";
      item.className = "source-tree-entry source-tree-folder";
      item.setAttribute("role", "treeitem");
      item.setAttribute("aria-expanded", String(!state.collapsedSourceFolders.has(folder)));
      item.style.paddingLeft = `${0.35 + depth * 0.8}rem`;
      const icon = document.createElement("span");
      icon.className = "source-tree-icon";
      icon.innerHTML = `<svg aria-hidden="true"><use href="#icon-folder"></use></svg>`;
      const label = document.createElement("span");
      label.className = "source-tree-label";
      label.textContent = folder.split("/").pop();
      const meta = document.createElement("small");
      meta.className = "source-tree-meta";
      meta.textContent = "Folder";
      item.append(icon, label, meta);
      item.title = folder;
      item.onclick = () => { if (state.collapsedSourceFolders.has(folder)) state.collapsedSourceFolders.delete(folder); else state.collapsedSourceFolders.add(folder); renderSourceTree(); };
      tree.append(item);
    }
    if (folder && state.collapsedSourceFolders.has(folder)) return;
    const children = [...folders].filter(candidate => candidate && parentPath(candidate) === folder).sort();
    for (const child of children) appendFolder(child, depth + 1);
    for (const source of sources.filter(candidate => parentPath(candidate.relPath) === folder).sort((a, b) => a.relPath.localeCompare(b.relPath))) appendSource(source, depth + 1);
  };
  appendFolder("", 0);
  if (!tree.children.length) tree.textContent = "No matching Markdown sources.";
}
async function loadCatalog(preferred = state.path) {
  const data = await responseJSON(await fetch("/_obsite/sources"));
  state.catalogSources = data.sources;
  const select = $("#source");
  select.replaceChildren();
  for (const source of data.sources) {
    const option = document.createElement("option");
    option.value = source.relPath;
    option.dataset.search = `${source.relPath} ${source.title} ${source.effectivePublish ? "published" : "draft"}`.toLowerCase();
    option.textContent = `${source.effectivePublish ? "" : "[Draft] "}${source.relPath}`;
    select.append(option);
  }
  renderSourceTree();
  filterDocuments();
  select.value = data.sources.some(source => source.relPath === preferred) ? preferred : (data.sources[0]?.relPath || "");
  if (select.value) await loadSource(select.value);
  else clearEditor();
  await refreshFiles(preferred);
}
function filterDocuments() {
  const query = ($("#document-search")?.value || "").trim().toLowerCase();
  for (const option of document.querySelectorAll("#source option")) option.hidden = Boolean(query) && !option.dataset.search.includes(query);
  renderSourceTree();
}

async function refreshFiles(preferred = state.filePath) {
  const data = await responseJSON(await fetch("/_obsite/files"));
  const tree = $("#file-tree");
  tree.replaceChildren();
  const selectionPath = preferred;
  if (selectionPath) for (const folder of state.collapsedFolders) if (selectionPath.startsWith(`${folder}/`)) state.collapsedFolders.delete(folder);
  let selected = null;
  for (const entry of data.entries) {
    if ([...state.collapsedFolders].some(folder => entry.path !== folder && entry.path.startsWith(`${folder}/`))) continue;
    const button = document.createElement("button");
    button.type = "button";
    button.className = "file-entry";
    button.dataset.path = entry.path;
    button.setAttribute("role", "treeitem");
    button.setAttribute("aria-selected", String(entry.path === preferred || entry.path === state.filePath));
    if (entry.kind === "folder") button.setAttribute("aria-expanded", String(!state.collapsedFolders.has(entry.path)));
    button.style.paddingLeft = `${0.35 + entry.path.split("/").length * 0.7}rem`;
    const icon = document.createElement("span");
    icon.className = "file-entry-icon";
    const fileIcon = entry.kind === "folder" ? "folder" : (/\.(png|jpe?g|webp|svg)$/i.test(entry.path) ? "image" : "code");
    icon.innerHTML = `<svg aria-hidden="true"><use href="#icon-${fileIcon}"></use></svg>`;
    const label = document.createElement("span");
    label.className = "file-entry-label";
    label.textContent = entry.path;
    button.append(icon, label);
    button.onclick = () => {
      if (entry.kind === "folder") {
        if (state.collapsedFolders.has(entry.path)) state.collapsedFolders.delete(entry.path);
        else state.collapsedFolders.add(entry.path);
        selectFileEntry(entry);
        refreshFiles(entry.path).catch(error => status(`File list failed: ${error.message}`));
        return;
      }
      selectFileEntry(entry);
    };
    tree.append(button);
    if (entry.path === selectionPath) selected = entry;
  }
  if (!data.entries.length) tree.textContent = "No managed files.";
  if (selected) {
    state.filePath = selected.path;
    state.fileHash = selected.hash;
    state.fileKind = selected.kind;
    state.fileSourceKind = selected.sourceKind || "";
    state.fileEditable = selected.editable !== false;
    $("#upload-folder").value = selected.kind === "folder" ? selected.path : (selected.path.includes("/") ? selected.path.slice(0, selected.path.lastIndexOf("/")) : "");
  } else {
    state.filePath = "";
    state.fileHash = "";
    state.fileKind = "";
    state.fileSourceKind = "";
    state.fileEditable = false;
  }
  updateFileActions();
}
function selectFileEntry(entry) {
  if (entry.sourceKind && entry.path !== state.path && !confirmDiscard()) return;
  state.filePath = entry.path;
  state.fileHash = entry.hash;
  state.fileKind = entry.kind;
  state.fileSourceKind = entry.sourceKind || "";
  state.fileEditable = entry.editable !== false;
  $("#upload-folder").value = entry.kind === "folder" ? entry.path : (entry.path.includes("/") ? entry.path.slice(0, entry.path.lastIndexOf("/")) : "");
  document.querySelectorAll(".file-entry").forEach(item => item.setAttribute("aria-selected", String(item.dataset.path === entry.path)));
  updateFileActions();
  if (entry.sourceKind) loadSource(entry.path);
  else status(`Selected ${entry.path}`);
}
function updateFileActions() {
  const hasSelection = Boolean(state.filePath) && !state.busy;
  $("#file-rename").disabled = !hasSelection || !state.fileEditable;
  $("#file-delete").disabled = !hasSelection || !state.fileEditable;
}
function clearEditor() {
  state.path = "";
  state.kind = "article";
  state.source = "";
  state.baselineSource = "";
  state.hash = "";
  state.bodyBaseline = "";
  state.formBaseline = {};
  state.sourceChanged = false;
  showForm({publish: false});
  setText("");
  updateMode();
  dirty(false);
  diagnostics();
  $("#preview-frame").hidden = true;
  $("#preview-frame").removeAttribute("src");
  $("#preview-empty").hidden = false;
  status("No Markdown sources");
}
async function loadSource(path) {
  const token = ++state.loadToken;
  busy(true);
  try {
    const data = await responseJSON(await fetch(`/_obsite/source-meta?path=${encodeURIComponent(path)}`));
    if (token !== state.loadToken) return;
    state.path = path;
    renderSourceTree();
    state.kind = data.kind;
    state.hash = data.sourceHash;
    state.filePath = path;
    state.fileHash = data.sourceHash;
    state.fileKind = "file";
    state.fileSourceKind = data.kind;
    state.source = data.source;
    state.baselineSource = data.source;
    state.mode = data.parseError ? "source" : "visual";
    state.sourceChanged = false;
    showForm(data.frontmatter);
    setText(state.mode === "source" ? data.source : data.body);
    state.bodyBaseline = text();
    updateMode();
    dirty(false);
    diagnostics();
    $("#preview-frame").hidden = true;
    $("#preview-frame").removeAttribute("src");
    $("#preview-empty").hidden = false;
    status(data.parseError ? `Fix this in source mode: ${data.parseError}` : `Loaded ${path}`);
    await offerDraft(path, state.hash);
  } catch (error) { status(`Load failed: ${error.message}`); }
  finally {
    if (token === state.loadToken) {
      busy(false);
      if (state.keepPreview && state.previewOpen && !state.suppressPreview) queueMicrotask(() => preview().catch(() => {}));
    }
  }
}
function updateMode() {
  $("#metadata").hidden = state.mode === "source";
  $("#mode").textContent = state.mode === "source" ? "Form mode" : "Source mode";
  $("#write-mode").setAttribute("aria-selected", String(state.mode !== "source"));
  $("#source-mode").setAttribute("aria-selected", String(state.mode === "source"));
}
function originalOffsetForNormalized(value, target) {
  let normalized = 0;
  let offset = 0;
  while (offset < value.length && normalized < target) {
    if (value[offset] === "\r" && value[offset + 1] === "\n") offset++;
    offset++;
    normalized++;
  }
  return offset;
}
function sourceLineBreakAt(value, offset) {
  for (let index = offset - 1; index >= 0; index--) {
    if (value[index] === "\n") return index > 0 && value[index - 1] === "\r" ? "\r\n" : "\n";
    if (value[index] === "\r") return "\r";
  }
  for (let index = offset; index < value.length; index++) {
    if (value[index] === "\n") return index > 0 && value[index - 1] === "\r" ? "\r\n" : "\n";
    if (value[index] === "\r") return "\r";
  }
  return "\n";
}
function applySourceChanges(update) {
  const changes = [];
  update.changes.iterChanges((fromA, toA, _fromB, _toB, insert) => changes.push({fromA, toA, insert: insert.toString()}));
  let source = state.source;
  for (let index = changes.length - 1; index >= 0; index--) {
    const change = changes[index];
    const from = originalOffsetForNormalized(source, change.fromA);
    const to = originalOffsetForNormalized(source, change.toA);
    const lineBreak = sourceLineBreakAt(source, from);
    const inserted = change.insert.replace(/\n/g, lineBreak);
    source = source.slice(0, from) + inserted + source.slice(to);
  }
  return source;
}
async function compose() {
  if (state.mode === "source") return state.source;
  const changed = changes();
  const bodyChanged = text() !== state.bodyBaseline;
  if (!changed.length && !bodyChanged) return state.source;
  const request = {path: state.path, source: state.source};
  if (bodyChanged) request.body = text();
  if (changed.length) { request.fields = formValues(); request.changed = changed; }
  const result = await mutation("POST", "/_obsite/frontmatter", request);
  return result.source;
}
async function operation(label, action) {
  if (state.busy || !state.path) return;
  busy(true);
  try { await action(); }
  catch (error) {
    diagnostics(error.diagnostics, `${label} failed: ${error.message}`);
    saveState("failed", label === "Save" ? "Build failed" : `${label} failed`);
    status(label === "Save" ? "Save failed; see diagnostics." : `${label} failed; see diagnostics.`);
  }
  finally { busy(false); }
}
async function save() {
  await operation("Save", async () => {
    status("Validating, building, and saving…");
    saveState("saving", "Saving");
    const path = state.path;
    const oldHash = state.hash;
    const mode = state.mode;
    const reopenPreview = state.keepPreview && state.previewOpen;
    const source = await compose();
    const result = await mutation("PUT", `/_obsite/source?path=${encodeURIComponent(path)}`, source, {"X-Obsite-Source-Hash": state.hash});
    await clearDraft(path);
    dirty(false);
    state.suppressPreview = true;
    try { await loadCatalog(path); } finally { state.suppressPreview = false; }
    if (mode === "source") { state.mode = "source"; setText(state.source); updateMode(); }
    if (reopenPreview) {
      try {
        const previewResult = await mutation("POST", "/_obsite/preview", {path: state.path, source: state.source});
        const frame = $("#preview-frame");
        frame.hidden = false;
        frame.src = previewResult.contentURL || previewResult.url;
        frame.onload = bindPreviewSync;
        $("#preview-empty").hidden = true;
        state.previewOpen = true;
      } catch (error) { diagnostics(error.diagnostics, `Preview refresh failed: ${error.message}`); }
    }
    diagnostics(result.diagnostics);
    saveState("saved", result.warningCount ? "Saved with warnings" : "Saved");
    status(result.warningCount ? "Saved and rebuilt with warnings." : "Saved and rebuilt.");
  });
}
async function preview() {
  await operation("Preview", async () => {
    status("Generating server-rendered draft preview…");
    const result = await mutation("POST", "/_obsite/preview", {path: state.path, source: await compose()});
    const frame = $("#preview-frame");
    frame.hidden = false;
    state.previewOpen = true;
    $("#preview-empty").hidden = true;
    frame.src = result.contentURL || result.url;
    frame.onload = bindPreviewSync;
    diagnostics(result.diagnostics);
    status(result.warningCount ? "Content preview generated with warnings." : "Content preview generated; source and public output were not changed.");
  });
}
async function fullPagePreview() {
  if (!state.path) return;
  const popup = window.open("about:blank", "_blank");
  if (popup) popup.opener = null;
  let opened = false;
  await operation("Full page preview", async () => {
    if (!popup) throw new Error("Allow pop-ups to open the full page preview.");
    status("Generating full page preview…");
    const result = await mutation("POST", "/_obsite/preview", {path: state.path, source: await compose()});
    popup.location = result.fullURL || result.url;
    opened = true;
    diagnostics(result.diagnostics);
    status(result.warningCount ? "Full page preview generated with warnings." : "Full page preview opened; source and public output were not changed.");
  });
  if (!opened) popup?.close();
}
function sourceParts(source) {
  const match = source.match(/^(?:\uFEFF)?---(?:\r\n|\r|\n)([\s\S]*?)(?:\r\n|\r|\n)(?:---|\.\.\.)(?:\r\n|\r|\n|$)/);
  if (!match) return {fields: new Map(), body: source};
  const fields = new Map();
  const lines = match[1].split(/\r?\n/);
  for (let index = 0; index < lines.length; index++) {
    const field = lines[index].match(/^(?:"([^"]+)"|'([^']+)'|([A-Za-z][A-Za-z0-9_-]*)):[ \t]*(.*)$/);
    if (!field) continue;
    const name = field[1] || field[2] || field[3];
    let value = field[4] || "";
    if (value === "|" || value === ">") {
      const block = [];
      while (index + 1 < lines.length && /^(?:[ \t]+|$)/.test(lines[index + 1])) block.push(lines[++index].trim());
      value += ` ${block.join(value === "|" ? "\\n" : " ")}`;
    }
    fields.set(name, value);
  }
  return {fields, body: source.slice(match[0].length)};
}
function diffBlocks(before, after) {
  const rows = Array.from({length: before.length + 1}, () => Array(after.length + 1).fill(0));
  for (let i = before.length - 1; i >= 0; i--) for (let j = after.length - 1; j >= 0; j--) rows[i][j] = before[i] === after[j] ? rows[i + 1][j + 1] + 1 : Math.max(rows[i + 1][j], rows[i][j + 1]);
  const operations = [];
  let i = 0, j = 0;
  while (i < before.length || j < after.length) {
    if (i < before.length && j < after.length && before[i] === after[j]) { operations.push({state: "unchanged", value: before[i]}); i++; j++; }
    else if (j === after.length || (i < before.length && rows[i + 1][j] >= rows[i][j + 1])) { operations.push({state: "deleted", value: before[i++]}); }
    else { operations.push({state: "added", value: after[j++]}); }
  }
  for (let index = 0; index + 1 < operations.length; index++) {
    if (operations[index].state === "deleted" && operations[index + 1].state === "added") {
      operations.splice(index, 2, {state: "modified", value: `${operations[index].value} → ${operations[index + 1].value}`});
    }
  }
  return operations;
}
function appendStructuredDiff(host, baseline, candidate) {
  const before = sourceParts(baseline);
  const after = sourceParts(candidate);
  const fieldNames = [...new Set([...before.fields.keys(), ...after.fields.keys()])].sort();
  const fields = document.createElement("section");
  fields.className = "structured-diff";
  fields.innerHTML = "<h3>Frontmatter fields</h3>";
  const fieldList = document.createElement("ul");
  for (const name of fieldNames) {
    const oldValue = before.fields.get(name);
    const newValue = after.fields.get(name);
    const item = document.createElement("li");
    const stateName = oldValue === undefined ? "added" : newValue === undefined ? "deleted" : oldValue === newValue ? "unchanged" : "modified";
    item.className = `diff-${stateName}`;
    item.textContent = `${stateName}: ${name}${oldValue !== undefined ? ` · ${oldValue}` : ""}${newValue !== undefined && newValue !== oldValue ? ` → ${newValue}` : ""}`;
    fieldList.append(item);
  }
  if (!fieldNames.length) fieldList.textContent = "No frontmatter fields";
  fields.append(fieldList);
  const blocks = document.createElement("section");
  blocks.className = "structured-diff";
  blocks.innerHTML = "<h3>Markdown blocks</h3>";
  const blockList = document.createElement("ol");
  const oldBlocks = before.body.split(/\r?\n\s*\r?\n/).filter(Boolean);
  const newBlocks = after.body.split(/\r?\n\s*\r?\n/).filter(Boolean);
  const operations = diffBlocks(oldBlocks, newBlocks);
  for (const operation of operations) {
    const item = document.createElement("li");
    item.className = `diff-${operation.state}`;
    item.textContent = `${operation.state}: ${operation.value}`;
    blockList.append(item);
  }
  if (!operations.length) blockList.textContent = "No body blocks";
  blocks.append(blockList);
  host.append(fields, blocks);
}
async function appendRenderedDiff(host, baseline, candidate) {
  const rendered = document.createElement("section");
  rendered.className = "rich-diff";
  rendered.innerHTML = "<h3>Rendered Markdown</h3>";
  const panes = document.createElement("div");
  panes.className = "rich-diff-panes";
  for (const [label, source] of [["Saved", baseline], ["Candidate", candidate]]) {
    const preview = await responseJSON(await mutation("POST", "/_obsite/preview", {path: state.path, source}));
    const pane = document.createElement("div");
    const title = document.createElement("strong");
    title.textContent = label;
    const frame = document.createElement("iframe");
    frame.title = `${label} rendered Markdown`;
    frame.src = preview.contentURL || preview.url;
    pane.append(title, frame);
    panes.append(pane);
  }
  rendered.append(panes);
  host.append(rendered);
}
async function showDiff() {
  if (!state.path) return;
  const candidate = await compose();
  const host = $("#diff-editor");
  host.hidden = false;
  state.previewOpen = false;
  $("#preview-frame").hidden = true;
  $("#preview-empty").hidden = true;
  host.replaceChildren();
  if (state.diffView) state.diffView.destroy();
  if (candidate === state.baselineSource) {
    host.textContent = "No changes";
    return;
  }
  const heading = document.createElement("p");
  heading.className = "diff-summary";
  heading.textContent = `Frontmatter and body changes for ${state.path}`;
  host.append(heading);
  const mergeHost = document.createElement("div");
  mergeHost.className = "merge-host";
  host.append(mergeHost);
  state.diffView = new MergeView({
    a: {doc: state.baselineSource, extensions: [basicSetup, markdown()]},
    b: {doc: candidate, extensions: [basicSetup, markdown()]},
    parent: mergeHost,
  });
  appendStructuredDiff(host, state.baselineSource, candidate);
  appendRenderedDiff(host, state.baselineSource, candidate).catch(error => diagnostics(error.diagnostics, `Rendered diff failed: ${error.message}`));
}
function bindPreviewSync() {
  const frame = $("#preview-frame");
  if (!frame.contentWindow || !frame.contentDocument) return;
  frame.contentWindow.addEventListener("scroll", () => {
    if (!state.syncScroll || !editor || state.syncingScroll) return;
    const pageMax = Math.max(1, frame.contentDocument.documentElement.scrollHeight - frame.clientHeight);
    const editorMax = Math.max(1, editor.scrollDOM.scrollHeight - editor.scrollDOM.clientHeight);
    state.syncingScroll = true;
    editor.scrollDOM.scrollTop = frame.contentWindow.scrollY / pageMax * editorMax;
    requestAnimationFrame(() => { state.syncingScroll = false; });
  }, {passive: true});
}
function showPreviewSurface() {
  $("#diff-editor").hidden = true;
  $("#preview-tab").setAttribute("aria-selected", "true");
  $("#diff-tab").setAttribute("aria-selected", "false");
  if (!$("#preview-frame").src) $("#preview-empty").hidden = false;
  else $("#preview-frame").hidden = false;
}

async function toggleMode() {
  await operation("Switch mode", async () => {
    const source = await compose();
    if (state.mode === "visual") {
      state.source = source;
      state.sourceChanged = false;
      state.mode = "source";
      setText(source);
    } else {
      const result = await mutation("POST", "/_obsite/frontmatter", {path: state.path, source});
      if (result.parseError) throw new Error(result.parseError);
      state.source = source;
      state.mode = "visual";
      showForm(result.frontmatter);
      setText(result.body);
      state.bodyBaseline = text();
    }
    updateMode();
  });
}
function confirmDiscard() {
  if (!state.dirty) return true;
  const accepted = confirm("There are unsaved changes. Discard them?");
  if (accepted) {
    clearTimeout(state.draftTimer);
    state.dirty = false;
    saveState("saved", "Saved");
    clearDraft().catch(() => {});
  }
  return accepted;
}
function pathContains(parent, child) { return child === parent || child.startsWith(`${parent}/`); }
function insert(before, after = "") {
  if (state.busy) return;
  const {from, to} = editor.state.selection.main;
  const selected = editor.state.sliceDoc(from, to);
  const value = before + selected + after;
  editor.dispatch({changes: {from, to, insert: value}, selection: {anchor: from + before.length, head: from + before.length + selected.length}});
  editor.focus();
}
function prefixLines(prefix) {
  if (state.busy) return;
  const selection = editor.state.selection.main;
  const from = editor.state.doc.lineAt(selection.from).from;
  const to = editor.state.doc.lineAt(selection.to).to;
  const value = editor.state.sliceDoc(from, to).split("\n").map(line => prefix + line).join("\n");
  editor.dispatch({changes: {from, to, insert: value}, selection: {anchor: from, head: from + value.length}});
  editor.focus();
}
function command(name) {
  if (name === "bold") insert("**", "**");
  else if (name === "italic") insert("*", "*");
  else if (name === "code") insert("`", "`");
  else if (name === "heading") prefixLines("## ");
  else if (name === "quote") prefixLines("> ");
  else if (name === "ul") prefixLines("- ");
  else if (name === "ol") prefixLines("1. ");
  else if (name === "task") prefixLines("- [ ] ");
  else if (name === "link") {
    const url = prompt("Link URL", "https://");
    if (url) insert("[", `](${url})`);
  } else if (name === "image") openMedia();
}
function indentExtensions(spaces = state.indent) {
  return [indentUnit.of(" ".repeat(spaces)), EditorState.tabSize.of(spaces)];
}
const indentation = new Compartment();
function editorExtensions() {
  return [basicSetup, markdown(), indentation.of(indentExtensions()), EditorView.lineWrapping, editable.of(EditorView.editable.of(!state.busy)),
    Prec.highest(keymap.of([
      {key: "Mod-b", run: () => { command("bold"); return true; }},
      {key: "Mod-i", run: () => { command("italic"); return true; }},
      {key: "Mod-k", run: () => { command("link"); return true; }},
      {key: "Mod-s", run: () => { save(); return true; }},
      indentWithTab,
    ])),
    EditorView.contentAttributes.of({"aria-label": "Markdown content"}),
    EditorView.updateListener.of(update => {
      if (!suppressChanges && update.docChanged) {
        if (state.mode === "source") state.source = applySourceChanges(update);
        state.sourceChanged = true;
        updateEditorStats();
        dirty();
      }
    }),
  ];
}
function mediaReference(mediaPath) {
  const depth = state.path.split("/").length - 1;
  const destination = "../".repeat(depth) + mediaPath.split("/").map(encodeURIComponent).join("/");
  const alt = mediaPath.split("/").pop().replace(/\.[^.]+$/, "").replace(/[\[\]\\]/g, "\\$&");
  return `![${alt}](${destination})`;
}
function insertMedia(path) {
  if (state.busy) return;
  const reference = mediaReference(path);
  let {from, to} = editor.state.selection.main;
  let afterFrontmatter = false;
  if (state.mode === "source") {
    const source = text();
    const frontmatter = /^(?:\uFEFF)?---(?:\r\n|\r|\n)[\s\S]*?(?:\r\n|\r|\n)(?:---|\.\.\.)(?:(?:\r\n|\r|\n)|$)/.exec(source);
    if (frontmatter && from < frontmatter[0].length) {
      from = to = frontmatter[0].length;
      afterFrontmatter = true;
    } else if (/^(?:\uFEFF)?---[ \t]*(?:(?:\r\n|\r|\n)|$)/.test(source) && !frontmatter) {
      status("Fix the frontmatter or switch to form mode before inserting an image.");
      return;
    }
  }
  const source = text();
  const before = source.slice(0, from);
  const after = source.slice(to);
  const prefix = afterFrontmatter ? "\n" : (before && !before.endsWith("\n") ? "\n\n" : "");
  const value = prefix + reference + (after && !after.startsWith("\n") ? "\n\n" : "\n");
  editor.dispatch({changes: {from, to, insert: value}, selection: {anchor: from + value.length}});
  editor.focus();
}
async function openMedia() { $("#media-panel").hidden = false; await loadMedia(); }
async function loadMedia() {
  const list = $("#media-list");
  list.textContent = "Loading…";
  try {
    const data = await responseJSON(await fetch("/_obsite/media"));
    list.replaceChildren();
    if (!data.items.length) list.textContent = "No images yet.";
    for (const media of data.items) {
      const item = document.createElement("div"); item.className = "media-item";
      const image = document.createElement("img"); image.src = media.url; image.alt = media.path; image.loading = "lazy"; item.append(image);
      const addButton = (label, action) => { const button = document.createElement("button"); button.type = "button"; button.textContent = label; button.onclick = action; item.append(button); };
      addButton(media.name, () => insertMedia(media.path));
      for (const [field, label] of [["cover", "Set cover"], ["banner", "Set banner"]]) {
        if (state.mode === "source" || (state.kind === "section" && field === "cover")) continue;
        addButton(label, () => { $(`[data-field="${field}"]`).value = media.path; dirty(); });
      }
      list.append(item);
    }
  } catch (error) { list.textContent = `Media library failed to load: ${error.message}`; }
}
async function uploadMedia(file) {
  if (!file || state.busy) return;
  if (file instanceof FileList && file.length !== 1) { status("Upload exactly one file."); return; }
  try {
    const form = new FormData();
    form.append("file", file);
    const folder = $("#upload-folder").value.trim() || (state.fileKind === "folder" ? state.filePath : "");
    if (folder) form.append("folder", folder);
    if (/\.md$/iu.test(file.name)) form.append("kind", /^_index\.md$/iu.test(file.name) ? "section" : "article");
    const result = await mutation("POST", "/_obsite/media", form, {"X-Obsite-File-Hash": "absent"});
    await loadMedia();
    await refreshFiles(result.path);
    if (/\.md$/iu.test(file.name)) await loadCatalog(result.path);
    else insertMedia(result.path);
    status(`Uploaded ${result.path} and rebuilt successfully.`);
  } catch (error) { diagnostics(error.diagnostics, `Upload failed: ${error.message}`); saveState("failed", "Build failed"); status(`Upload failed: ${error.message}`); }
  finally { $("#media-upload").value = ""; }
}
function openFileDialog(mode) {
  if (state.busy) return;
  if ((mode === "rename" || mode === "delete") && !state.filePath) return;
  state.fileDialogMode = mode;
  const dialog = $("#file-dialog");
  const markdown = mode === "markdown";
  $("#file-dialog-title").textContent = mode === "folder" ? "New folder" : mode === "rename" ? "Rename" : "New Markdown file";
  $("#file-submit").textContent = mode === "folder" ? "Create folder" : mode === "rename" ? "Rename" : "Create file";
  $("#file-original").value = state.filePath;
  $("#file-path").value = mode === "rename" ? state.filePath : "";
  $("#file-path").readOnly = false;
  $("#markdown-file-fields").hidden = !markdown;
  $("#file-title").required = markdown;
  if (markdown) {
    $("#file-title").value = "";
    $("#file-type").value = "doc";
    $("#file-date").value = "";
    $("#file-kind").value = "article";
  }
  dialog.showModal();
}
async function runFileMutation(label, action, preferred = state.filePath, reloadSource = true) {
  if (state.busy) return;
  busy(true);
  try {
    await action();
    if (reloadSource) {
      dirty(false);
      await loadCatalog(preferred);
    } else {
      await refreshFiles(preferred);
    }
    status(`${label} completed and rebuilt.`);
  } catch (error) {
    diagnostics(error.diagnostics, `${label} failed: ${error.message}`);
    saveState("failed", `${label} failed`);
    status(`${label} failed; see diagnostics.`);
    throw error;
  } finally {
    busy(false);
  }
}

document.querySelectorAll("button svg, label svg, .workspace-search svg").forEach(svg => { svg.setAttribute("aria-hidden", "true"); svg.setAttribute("focusable", "false"); });
editor = new EditorView({state: EditorState.create({extensions: editorExtensions()}), parent: $("#editor")});
editor.scrollDOM.addEventListener("scroll", () => {
  if (!state.syncScroll || state.syncingScroll || !$("#preview-frame").contentWindow) return;
  const frame = $("#preview-frame");
  const editorMax = Math.max(1, editor.scrollDOM.scrollHeight - editor.scrollDOM.clientHeight);
  const pageMax = Math.max(1, frame.contentDocument?.documentElement?.scrollHeight - frame.clientHeight || 1);
  state.syncingScroll = true;
  frame.contentWindow.scrollTo(0, editor.scrollDOM.scrollTop / editorMax * pageMax);
  requestAnimationFrame(() => { state.syncingScroll = false; });
}, {passive: true});
loadPreferences();
$("#document-search").oninput = filterDocuments;
$("#sidebar-toggle").onclick = event => { const open = $(".source-sidebar").classList.toggle("drawer-open"); event.currentTarget.setAttribute("aria-expanded", String(open)); event.currentTarget.setAttribute("aria-label", open ? "Close documents" : "Open documents"); };
document.addEventListener("keydown", event => { if (event.key === "Escape" && $(".source-sidebar").classList.contains("drawer-open")) { $(".source-sidebar").classList.remove("drawer-open"); $("#sidebar-toggle").setAttribute("aria-expanded", "false"); $("#sidebar-toggle").setAttribute("aria-label", "Open documents"); } });
$("#media-nav").onclick = openMedia;
$("#settings").onclick = () => $("#settings-dialog").showModal();
$("#settings-save").onclick = storePreferences;
$("#help").onclick = () => status("Shortcuts: Ctrl/Cmd+B bold, Ctrl/Cmd+I italic, Ctrl/Cmd+K link, Ctrl/Cmd+S save.");
$("#logout").onclick = async () => { try { await mutation("POST", "/_obsite/logout"); location.href = "/_obsite/login"; } catch (error) { status(`Log out failed: ${error.message}`); } };
$("#write-mode").onclick = () => { if (state.mode === "source") toggleMode(); };
$("#source-mode").onclick = () => { if (state.mode !== "source") toggleMode(); };
$("#preview-tab").onclick = showPreviewSurface;
$("#diff-tab").onclick = async () => { $("#preview-tab").setAttribute("aria-selected", "false"); $("#diff-tab").setAttribute("aria-selected", "true"); try { await showDiff(); } catch (error) { diagnostics(error.diagnostics, `Diff failed: ${error.message}`); } };
$("#preview-refresh").onclick = preview;
$("#preview-new-window").onclick = fullPagePreview;
$("#sync-scroll").onclick = event => { state.syncScroll = !state.syncScroll; event.currentTarget.setAttribute("aria-pressed", String(state.syncScroll)); };
$("#device-desktop").onclick = () => { $("#preview-frame").classList.remove("preview-mobile", "preview-tablet"); $("#device-desktop").classList.add("active"); $("#device-tablet").classList.remove("active"); $("#device-mobile").classList.remove("active"); };
$("#device-tablet").onclick = () => { $("#preview-frame").classList.remove("preview-mobile"); $("#preview-frame").classList.add("preview-tablet"); $("#device-tablet").classList.add("active"); $("#device-desktop").classList.remove("active"); $("#device-mobile").classList.remove("active"); };
$("#device-mobile").onclick = () => { $("#preview-frame").classList.remove("preview-tablet"); $("#preview-frame").classList.add("preview-mobile"); $("#device-mobile").classList.add("active"); $("#device-desktop").classList.remove("active"); $("#device-tablet").classList.remove("active"); };
$("#source").onchange = event => {
  if (confirmDiscard()) loadSource(event.target.value);
  else event.target.value = state.path;
};
$("#refresh").onclick = () => { if (confirmDiscard()) loadCatalog().catch(error => status(error.message)); };
$("#save").onclick = save;
$("#preview").onclick = preview;
$("#full-page-preview").onclick = fullPagePreview;
$("#toolbar-preview").onclick = preview;
$("#toolbar-split").onclick = event => {
  const grid = $(".editor-preview-grid");
  const focused = grid.classList.toggle("focus-preview");
  event.currentTarget.setAttribute("aria-pressed", String(focused));
};
$("#toolbar-fullscreen").onclick = event => {
  const fullscreen = document.body.classList.toggle("editor-fullscreen");
  event.currentTarget.setAttribute("aria-pressed", String(fullscreen));
};
$("#toolbar-help").onclick = () => status("Shortcuts: Ctrl/Cmd+B bold, Ctrl/Cmd+I italic, Ctrl/Cmd+K link, Ctrl/Cmd+S save.");
$("#mode").onclick = toggleMode;
$("#metadata").oninput = () => dirty();
$("#metadata").onsubmit = event => event.preventDefault();
$("#file-refresh").onclick = () => refreshFiles().catch(error => status(`File list failed: ${error.message}`));
$("#file-new-folder").onclick = () => openFileDialog("folder");
$("#file-new-markdown").onclick = () => openFileDialog("markdown");
$("#file-rename").onclick = () => openFileDialog("rename");
$("#file-delete").onclick = () => {
  if (!state.filePath || !confirmDiscard()) return;
  const qualifier = state.fileKind === "folder" ? " (empty folder)" : "";
  if (!confirm(`Delete ${state.filePath}${qualifier}?`)) return;
  const pathValue = state.filePath;
  const fileHash = state.fileHash;
  const affectsCurrent = pathContains(pathValue, state.path);
  const preferred = affectsCurrent ? "" : state.path;
  runFileMutation("Delete", async () => {
    await mutation("DELETE", `/_obsite/file?path=${encodeURIComponent(pathValue)}&confirm=true`, null, {"X-Obsite-File-Hash": fileHash});
    await clearDraft(pathValue);
  }, preferred, affectsCurrent).catch(() => {});
};
$("#file-form").onsubmit = async event => {
  if (event.submitter?.value === "cancel") return;
  event.preventDefault();
  const mode = state.fileDialogMode;
  const pathValue = $("#file-path").value.trim();
  const dialog = $("#file-dialog");
  try {
    if (mode === "folder") {
      await runFileMutation("Create folder", () => mutation("POST", "/_obsite/file/folder", {path: pathValue}, {"X-Obsite-File-Hash": "absent"}), pathValue, false);
    } else if (mode === "markdown") {
      const request = {path: pathValue, title: $("#file-title").value, type: $("#file-type").value, date: $("#file-date").value, kind: $("#file-kind").value};
      await runFileMutation("Create Markdown file", () => mutation("POST", "/_obsite/file/markdown", request, {"X-Obsite-File-Hash": "absent"}), pathValue);
    } else if (mode === "rename") {
      const oldPath = $("#file-original").value;
      const affectsCurrent = pathContains(oldPath, state.path);
      if (affectsCurrent && !confirmDiscard()) return;
      const suffix = affectsCurrent ? state.path.slice(oldPath.length) : "";
      const preferred = affectsCurrent ? pathValue + suffix : state.path;
      await runFileMutation("Rename", () => mutation("PUT", `/_obsite/file?path=${encodeURIComponent(oldPath)}`, {destination: pathValue}, {"X-Obsite-File-Hash": state.fileHash}), preferred, affectsCurrent);
    }
    dialog.close();
  } catch (error) {
    diagnostics(error.diagnostics, error.message);
    saveState("failed", "Build failed");
    status(`${mode === "rename" ? "Rename" : "File operation"} failed: ${error.message}`);
  }
};
$("#new").onclick = () => { if (confirmDiscard()) $("#new-dialog").showModal(); };
$("#new-form").onsubmit = async event => {
  if (event.submitter?.value === "cancel") return;
  event.preventDefault();
  const form = new URLSearchParams(new FormData(event.target));
  try {
    await mutation("POST", "/_obsite/source", form, {"X-Obsite-Source-Hash": "absent"});
    $("#new-dialog").close(); dirty(false);
    await loadCatalog(form.get("path"));
  } catch (error) { diagnostics(error.diagnostics, error.message); saveState("failed", "Build failed"); $("#new-dialog").close(); status(`Create failed: ${error.message}`); }
};
$("#delete").onclick = () => operation("Delete", async () => {
  if (state.kind !== "article" || !confirm("Delete this article?")) return;
  const deletedPath = state.path;
  await mutation("DELETE", `/_obsite/source?path=${encodeURIComponent(deletedPath)}&confirm=true`, null, {"X-Obsite-Source-Hash": state.hash});
  await clearDraft(deletedPath);
  dirty(false); await loadCatalog(""); status("Deleted and rebuilt.");
});
$("#media").onclick = openMedia;
$("#media-close").onclick = () => { $("#media-panel").hidden = true; };
$("#media-upload").onchange = event => { if (event.target.files.length !== 1) { saveState("failed", "Upload failed"); diagnostics([], "Upload exactly one file; no files were written."); status("Upload exactly one file; no files were written."); event.target.value = ""; return; } uploadMedia(event.target.files[0]); };
document.querySelectorAll("[data-command]").forEach(button => {
  button.onmousedown = event => event.preventDefault();
  button.onclick = () => command(button.dataset.command);
});
window.addEventListener("beforeunload", event => { if (state.dirty) { event.preventDefault(); event.returnValue = ""; } });
loadCatalog().catch(error => status(`Editor unavailable: ${error.message}`));
