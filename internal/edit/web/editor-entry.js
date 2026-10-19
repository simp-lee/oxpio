import {basicSetup} from "codemirror";
import {markdown} from "@codemirror/lang-markdown";
import {indentUnit} from "@codemirror/language";
import {indentWithTab} from "@codemirror/commands";
import {Compartment, EditorState, Prec, StateEffect, StateField} from "@codemirror/state";
import {Decoration, EditorView, WidgetType, keymap} from "@codemirror/view";
import {MergeView} from "@codemirror/merge";

const $ = selector => document.querySelector(selector);
const fields = [...document.querySelectorAll("[data-field]")];
const sectionFields = new Set(["title", "publish", "description", "order", "banner", "bannerAlt"]);
const state = {
  path: new URLSearchParams(location.search).get("path") || "",
  kind: "article", mode: "visual", surface: "write", source: "", baselineSource: "", hash: "",
  bodyBaseline: "", formBaseline: {}, sourceChanged: false,
  filePath: "", fileHash: "", fileKind: "", fileSourceKind: "",
  fileDialogMode: "", dirty: false, busy: false, busyCount: 0, busyReason: "", operationController: null, operationOwned: false, operationCommitted: false, operationCancelled: false, livePreviewBusy: false, livePreviewBusyRequest: 0, livePreviewController: null, livePreviewFrameCancel: null, loadToken: 0,
  draftTimer: 0, diffView: null, diffWrap: false, previewURL: "", previewContentURL: "", previewPageURL: "", previewKind: "content", previewResizeObserver: null, fileEditable: false, indent: 2, keepPreview: true, previewOpen: false, suppressPreview: false, previewPending: "", livePreviewAfterCommit: false, livePreviewAfterCommitRequest: 0, previewToken: "", catalogSources: [], sourceTreeInitialized: false, collapsedFolders: new Set(), collapsedSourceFolders: new Set(),
};
const editable = new Compartment();
let editor;
let suppressChanges = false;
let livePreviewTimer = 0;
let livePreviewRequest = 0;
const requestTimeout = 30000;

function status(message) { $("#status").textContent = message; }
function isAbortError(error) { return error && error.name === "AbortError"; }
function markOperationCommitted(label = "Operation") {
  state.operationCommitted = true;
  state.operationController = new AbortController();
  state.operationOwned = true;
  state.busyReason = `${label} committed; refreshing…`;
  syncBusyIndicator();
}
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
  document.title = value ? "* OXPIO Editor" : "OXPIO Editor";
  saveState(value ? "unsaved" : "saved", value ? "Unsaved" : "Saved");
  if (value) scheduleDraft();
}
function cancelLivePreview(notify = false) {
  clearTimeout(livePreviewTimer);
  livePreviewTimer = 0;
  livePreviewRequest++;
  if (state.livePreviewController) {
    state.livePreviewController.abort();
    state.livePreviewController = null;
  }
  if (state.livePreviewFrameCancel) {
    state.livePreviewFrameCancel();
    state.livePreviewFrameCancel = null;
    const frame = $("#preview-frame");
    frame.onload = null;
    frame.src = "about:blank";
    frame.hidden = true;
  }
  state.livePreviewBusy = false;
  state.livePreviewBusyRequest = 0;
  syncBusyIndicator();
  if (notify) status("Live preview cancelled.");
}
function scheduleLivePreview(delay = 350) {
  const afterCommit = state.livePreviewAfterCommit;
  if (state.surface !== "write" || state.mode !== "visual" || !editor) {
    cancelLivePreview();
    return;
  }
  cancelLivePreview();
  state.livePreviewAfterCommit = false;
  if (!afterCommit) diagnostics();
  const request = ++livePreviewRequest;
  state.livePreviewAfterCommitRequest = afterCommit ? request : 0;
  editor.dispatch({effects: [livePreviewLine.of(null), livePreviewRender.of({body: "", blocks: [], styles: ""})]});
  livePreviewTimer = setTimeout(() => refreshLivePreview(request).catch(error => diagnostics(error.diagnostics, `Live preview failed: ${error.message}`)), delay);
}
function previewTokenFromURL(url) {
  return String(url || "").match(/\/[_a-z0-9-]*oxpio\/preview\/([^/]+)/iu)?.[1] || "";
}
function previewRequestHeaders() {
  return state.previewToken ? {"X-OXPIO-Preview-Token": state.previewToken} : {};
}
function previewURLFor(kind) {
  return kind === "page" ? state.previewPageURL || state.previewContentURL : state.previewContentURL || state.previewPageURL;
}
function resetPreviewFrame() {
  state.previewResizeObserver?.disconnect();
  state.previewResizeObserver = null;
  state.previewOpen = false;
  state.previewURL = "";
  state.previewContentURL = "";
  state.previewPageURL = "";
  const frame = $("#preview-frame");
  frame.onload = null;
  frame.hidden = true;
  frame.removeAttribute("src");
  frame.style.height = "";
}
function loadPreviewFrame(url, signal) {
  const frame = $("#preview-frame");
  return new Promise((resolve, reject) => {
    let settled = false;
    const finish = error => {
      if (settled) return;
      settled = true;
      clearTimeout(timeout);
      frame.removeEventListener("load", onload);
      frame.removeEventListener("error", onerror);
      signal?.removeEventListener("abort", onabort);
      if (error) reject(error); else resolve();
    };
    const onload = () => {
      bindPreviewFrame();
      const documentRoot = frame.contentDocument?.documentElement;
      const invalid = !documentRoot || documentRoot.getAttribute("data-oxpio-kind") === "404";
      finish(invalid ? new Error("Preview frame did not contain a rendered page") : null);
    };
    const onerror = () => finish(new Error("Preview frame failed to load"));
    const onabort = () => { frame.src = "about:blank"; finish(new DOMException("Preview cancelled", "AbortError")); };
    const timeout = setTimeout(() => {
      const error = new Error("Preview frame timed out");
      error.name = "TimeoutError";
      finish(error);
    }, requestTimeout);
    if (signal?.aborted) {
      onabort();
      return;
    }
    if (signal) signal.addEventListener("abort", onabort, {once: true});
    frame.addEventListener("load", onload, {once: true});
    frame.addEventListener("error", onerror, {once: true});
    frame.hidden = false;
    frame.src = url;
  });
}
async function applyPreviewResult(result, kind = "content") {
  state.previewContentURL = result.contentURL || result.url || "";
  state.previewPageURL = result.fullURL || result.url || "";
  state.previewToken = previewTokenFromURL(state.previewContentURL || state.previewPageURL);
  state.previewKind = kind;
  state.previewURL = previewURLFor(kind);
  state.previewOpen = true;
  const empty = $("#preview-empty");
  empty.hidden = false;
  empty.classList.add("is-loading");
  empty.textContent = "Loading preview…";
  updateMode();
  await loadPreviewFrame(state.previewURL, state.operationController?.signal);
  empty.hidden = true;
  empty.classList.remove("is-loading");
}
async function refreshLivePreview(request) {
  if (request !== livePreviewRequest || state.surface !== "write" || state.mode !== "visual" || !state.path || state.busy) return;
  state.livePreviewBusy = true;
  state.livePreviewBusyRequest = request;
  const controller = new AbortController();
  let cancelFrameWait;
  let livePreviewDiagnostics = [];
  let generatedPreviewToken = "";
  state.livePreviewController = controller;
  syncBusyIndicator();
  try {
    const source = await compose(controller.signal);
    if (request !== livePreviewRequest || state.surface !== "write" || state.mode !== "visual" || state.busy) return;
    const result = await mutation("POST", "/_oxpio/preview", {path: state.path, source}, previewRequestHeaders(), controller.signal);
    livePreviewDiagnostics = result.diagnostics || [];
    const resultToken = previewTokenFromURL(result.contentURL || result.url || result.fullURL);
    if (request !== livePreviewRequest || state.surface !== "write" || state.mode !== "visual" || state.busy) {
      await cleanupPreviewToken(resultToken);
      return;
    }
    diagnostics(livePreviewDiagnostics);
    const frame = $("#preview-frame");
    state.previewContentURL = result.contentURL || result.url || "";
    state.previewPageURL = result.fullURL || result.url || "";
    state.previewToken = resultToken;
    generatedPreviewToken = resultToken;
    state.previewKind = "content";
    state.previewURL = state.previewContentURL;
    state.previewOpen = true;
    const loaded = new Promise((resolve, reject) => {
      let settled = false;
      const finish = (error = null) => {
        if (settled) return;
        settled = true;
        window.clearTimeout(timeout);
        frame.removeEventListener("load", onload);
        frame.removeEventListener("error", onerror);
        if (error) reject(error); else resolve();
      };
      const onload = () => finish();
      const onerror = () => finish(new Error("Live preview frame failed to load"));
      cancelFrameWait = () => finish(new DOMException("Live preview cancelled", "AbortError"));
      state.livePreviewFrameCancel = cancelFrameWait;
      const timeout = window.setTimeout(() => {
        const error = new Error("Live preview frame timed out");
        error.name = "TimeoutError";
        finish(error);
      }, requestTimeout);
      frame.addEventListener("load", onload, {once: true});
      frame.addEventListener("error", onerror, {once: true});
      frame.src = state.previewURL;
    });
    frame.hidden = false;
    await loaded;
    if (request !== livePreviewRequest || state.surface !== "write" || state.mode !== "visual" || state.busy) return;
    const documentRoot = frame.contentDocument?.querySelector("[data-page-content]");
    if (!documentRoot) throw new Error("Live preview page did not contain rendered content");
    const blocks = [...documentRoot.children].map(child => serializeLivePreviewBlock(child, frame.contentDocument));
    const styles = livePreviewStyles(frame.contentDocument);
    editor.dispatch({effects: livePreviewRender.of({body: text(), blocks, styles})});
    if (state.livePreviewAfterCommitRequest === request) state.livePreviewAfterCommitRequest = 0;
  } catch (error) {
    const currentRequest = request === livePreviewRequest && state.surface === "write" && state.mode === "visual" && !state.busy;
    if (generatedPreviewToken) {
      await cleanupPreviewToken(generatedPreviewToken);
      if (state.previewToken === generatedPreviewToken) state.previewToken = "";
    }
    if (!currentRequest) return;
    resetPreviewFrame();
    const afterCommit = state.livePreviewAfterCommitRequest === request;
    if (afterCommit) {
      saveState("saved", "Saved with warnings");
      status(isAbortError(error) ? "Saved; live preview cancelled." : "Saved; live preview failed; see diagnostics.");
      state.livePreviewAfterCommitRequest = 0;
    }
    if (livePreviewDiagnostics.length && !error.diagnostics) error.diagnostics = livePreviewDiagnostics;
    if (error.name !== "AbortError") {
      if (!afterCommit) status("Live preview failed; see diagnostics.");
      throw error;
    }
  } finally {
    if (state.livePreviewFrameCancel === cancelFrameWait) state.livePreviewFrameCancel = null;
    if (state.livePreviewBusyRequest === request) {
      if (state.livePreviewController === controller) state.livePreviewController = null;
      state.livePreviewBusy = false;
      syncBusyIndicator();
    }
  }
}
function scheduleDraft() {
  clearTimeout(state.draftTimer);
  if (!state.path || !window.indexedDB) return;
  saveState("unsaved", "Unsaved");
  state.draftTimer = setTimeout(() => saveDraft().catch(() => status("Local draft could not be saved; check browser storage.")), 1500);
}
function syncBusyIndicator() {
  const indicator = $("#busy-indicator");
  if (!indicator) return;
  const active = state.busy || state.livePreviewBusy;
  indicator.hidden = !active;
  if (active) {
    $("#busy-message").textContent = state.busy ? state.busyReason : "Updating live preview…";
    $("#cancel-operation").hidden = !(state.operationController || state.livePreviewBusy);
  } else {
    $("#cancel-operation").hidden = true;
  }
}
function busy(value, reason = "Working… controls are temporarily disabled.") {
  if (value && !state.busy) {
    cancelLivePreview();
    if (!state.operationController) {
      state.operationController = new AbortController();
      state.operationOwned = true;
    }
  }
  state.busyCount = Math.max(0, state.busyCount + (value ? 1 : -1));
  state.busy = state.busyCount > 0;
  if (value) state.busyReason = reason;
  else if (!state.busy) {
    state.busyReason = "";
    if (state.operationOwned) {
      state.operationController = null;
      state.operationOwned = false;
    }
    state.operationCommitted = false;
  }
  document.querySelectorAll(".header-actions button, #source, #refresh, #file-refresh, .file-actions button, .toolbar button, .editor-tabs button, .preview-actions button, .source-tree-entry, .file-entry, .workspace-search input, .new-form input, .new-form select, .new-form button, #metadata input, #metadata select, #media-panel button, #media-panel input, #media-panel select").forEach(input => { input.disabled = state.busy; });
  $("#content-editor").setAttribute("aria-busy", String(state.busy));
  syncBusyIndicator();
  $("#delete").disabled = state.busy || state.kind !== "article";
  updateFileActions();
  editor.dispatch({effects: editable.reconfigure(EditorView.editable.of(!state.busy))});
  if (!state.busy) flushPendingPreview();
}
function flushPendingPreview() {
  if (state.busy || !state.previewPending) return;
  const pending = state.previewPending;
  state.previewPending = "";
  if (pending === "live") scheduleLivePreview();
  else if (pending === "full") queueMicrotask(() => preview().catch(() => {}));
}
function progressStatus(message) {
  const started = Date.now();
  const update = () => status(`${message} (${Math.floor((Date.now() - started) / 1000)}s)`);
  update();
  const timer = setInterval(update, 500);
  return () => clearInterval(timer);
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
    const request = indexedDB.open("oxpio-editor", 1);
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
async function offerDraft(path, hash, token) {
  const current = () => token === state.loadToken && state.path === path && state.hash === hash;
  if (!window.indexedDB || !current()) return;
  const exact = await draftRecord(`${path}:${hash}`);
  if (!current()) return;
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
  if (!record || !record.candidate || !current()) return;
  if (confirm(`Restore the unsaved local draft for ${path}?`)) {
    if (!current()) return;
    const baselineFields = JSON.parse(JSON.stringify(state.formBaseline));
    state.mode = record.candidate.mode || "source";
    state.surface = state.mode === "source" ? "source" : "write";
    if (state.mode === "source") {
      state.source = record.candidate.source || state.source;
      setText(state.source);
    } else {
      showForm(record.candidate.fields || formValues());
      state.formBaseline = baselineFields;
      setText(record.candidate.body || "");
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
    const preferences = JSON.parse(localStorage.getItem("oxpio-editor-preferences") || "{}");
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
  localStorage.setItem("oxpio-editor-preferences", JSON.stringify({...preferences, indent: state.indent}));
  document.documentElement.dataset.theme = preferences.theme === "system" ? "" : preferences.theme;
  document.documentElement.style.colorScheme = preferences.theme === "dark" ? "dark" : preferences.theme === "light" ? "light" : "";
  $("#indent-setting").textContent = `Indent: ${state.indent} spaces`;
  applyIndentPreference();
}function dateInputValue(value) {
  const match = String(value ?? "").trim().match(/^(\d{4}-\d{2}-\d{2})/);
  return match ? match[1] : "";
}
function formValues() {
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
    else if (input.type === "date") input.value = dateInputValue(value);
    else input.value = value == null ? "" : String(value);
    input.closest("label").hidden = state.kind === "section" && !sectionFields.has(name);
  }
  state.formBaseline = formValues();
  $("#metadata-title").textContent = metadata.title || state.path || "Current source";
  const catalogSource = state.catalogSources.find(source => source.relPath === state.path);
  const published = catalogSource ? catalogSource.effectivePublish === true : metadata.publish === true;
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
  let timer;
  let body;
  try {
    body = await Promise.race([
      response.text(),
      new Promise((_, reject) => {
        timer = window.setTimeout(() => {
          response.__oxpioCancel?.();
          const timeoutError = new Error("Response body timed out");
          timeoutError.name = "TimeoutError";
          reject(timeoutError);
        }, requestTimeout);
      }),
    ]);
  } finally {
    window.clearTimeout(timer);
    response.__oxpioCleanup?.();
  }
  const data = response.headers.get("Content-Type")?.includes("application/json") ? JSON.parse(body) : {};
  if (!response.ok) {
    const error = new Error(data.error || body || `Request failed (${response.status})`);
    error.diagnostics = data.diagnostics || [];
    throw error;
  }
  return data;
}
function postCommitWarning(result) {
  let warning = result?.outputCleanupWarning ? " Output cleanup needs attention." : "";
  if (result?.sourceCleanupWarning) warning += " Temporary source/backup cleanup needs attention.";
  return warning;
}
function fetchWithTimeout(url, options = {}) {
  const controller = new AbortController();
  const sourceSignal = options.signal || state.operationController?.signal;
  const abort = () => controller.abort();
  if (sourceSignal) {
    if (sourceSignal.aborted) controller.abort();
    else sourceSignal.addEventListener("abort", abort, {once: true});
  }
  let timedOut = false;
  const timer = window.setTimeout(() => { timedOut = true; controller.abort(); }, requestTimeout);
  const clearTimer = () => window.clearTimeout(timer);
  const cleanup = () => {
    clearTimer();
    sourceSignal?.removeEventListener("abort", abort);
  };
  const cancel = () => {
    controller.abort();
    cleanup();
  };
  return fetch(url, {...options, signal: controller.signal}).then(response => {
    clearTimer();
    response.__oxpioCleanup = cleanup;
    response.__oxpioCancel = cancel;
    return response;
  }, error => {
    cleanup();
    if (timedOut) {
      const timeoutError = new Error("Request timed out");
      timeoutError.name = "TimeoutError";
      throw timeoutError;
    }
    throw error;
  });
}
async function mutation(method, url, body, headers = {}, signal) {
  signal = signal || state.operationController?.signal;
  const session = await responseJSON(await fetchWithTimeout("/_oxpio/csrf", {signal}));
  headers = {...headers, "X-OXPIO-CSRF": session.csrf};
  if (body && !(body instanceof FormData) && !(body instanceof URLSearchParams) && typeof body !== "string") {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(body);
  }
  return responseJSON(await fetchWithTimeout(url, {method, headers, body, signal}));
}
async function cleanupPreviewToken(token) {
  if (!token) return;
  try {
    await mutation("DELETE", `/_oxpio/preview/${encodeURIComponent(token)}`, null, {}, new AbortController().signal);
  } catch {
    // Preview cleanup is best effort; the server retries tracked cleanup failures.
  }
}
function renderSourceTree() {
  const tree = $("#source-tree");
  tree.replaceChildren();
  const query = ($("#document-search")?.value || "").trim().toLowerCase();
  const sources = state.catalogSources.filter(source => !query || `${source.relPath} ${source.title} ${source.effectivePublish ? "published" : "draft"}`.toLowerCase().includes(query));
  const searchStatus = $("#document-search-status");
  const noun = state.catalogSources.length === 1 ? "document" : "documents";
  if (searchStatus) searchStatus.textContent = query ? `${sources.length} of ${state.catalogSources.length} ${noun}` : `${sources.length} ${noun}`;
  const parentPath = value => value.includes("/") ? value.slice(0, value.lastIndexOf("/")) : "";
  const folders = new Set([""]);
  for (const source of sources) {
    const segments = source.relPath.split("/");
    for (let index = 1; index < segments.length; index++) folders.add(segments.slice(0, index).join("/"));
  }
  const appendSource = (source, depth, parent) => {
    const item = document.createElement("button");
    item.type = "button";
    item.className = "source-tree-entry";
    item.disabled = state.busy;
    item.dataset.path = source.relPath;
    item.setAttribute("role", "treeitem");
    item.setAttribute("aria-selected", String(source.relPath === state.path));
    item.style.paddingLeft = `${0.35 + depth * 0.45}rem`;
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
    item.onclick = () => { if (!confirmDiscard()) return; $("#source").value = source.relPath; loadSource(source.relPath).catch(error => { if (!isAbortError(error)) status(`Load failed: ${error.message}`); }); };
    parent.append(item);
  };
  const appendFolder = (folder, depth, parent) => {
    if (folder) {
      const item = document.createElement("div");
      item.className = "source-tree-folder";
      item.setAttribute("role", "treeitem");
      item.setAttribute("aria-expanded", String(Boolean(query) || !state.collapsedSourceFolders.has(folder)));
      const toggle = document.createElement("button");
      toggle.type = "button";
      toggle.className = "source-tree-entry";
      toggle.disabled = state.busy;
      toggle.style.paddingLeft = `${0.35 + depth * 0.45}rem`;
      const icon = document.createElement("span");
      icon.className = "source-tree-icon";
      icon.innerHTML = `<svg aria-hidden="true"><use href="#icon-folder"></use></svg>`;
      const label = document.createElement("span");
      label.className = "source-tree-label";
      label.textContent = folder.split("/").pop();
      const meta = document.createElement("small");
      meta.className = "source-tree-meta";
      meta.textContent = "Folder";
      toggle.append(icon, label, meta);
      toggle.title = folder;
      toggle.onclick = () => {
        if (query) return;
        if (state.collapsedSourceFolders.has(folder)) state.collapsedSourceFolders.delete(folder); else state.collapsedSourceFolders.add(folder);
        renderSourceTree();
      };
      item.append(toggle);
      parent.append(item);
      if (!query && state.collapsedSourceFolders.has(folder)) return;
      const group = document.createElement("div");
      group.className = "source-tree-group";
      group.setAttribute("role", "group");
      item.append(group);
      parent = group;
    }
    const children = [...folders].filter(candidate => candidate && parentPath(candidate) === folder).sort();
    for (const child of children) appendFolder(child, depth + 1, parent);
    for (const source of sources.filter(candidate => parentPath(candidate.relPath) === folder).sort((a, b) => a.relPath.localeCompare(b.relPath))) appendSource(source, depth + 1, parent);
  };
  appendFolder("", 0, tree);
  if (!tree.children.length) tree.textContent = "No matching Markdown sources.";
}
function defaultCollapsedSourceFolders(sources, preferred) {
  const collapsed = new Set();
  for (const source of sources) {
    const segments = source.relPath.split("/");
    for (let index = 1; index < segments.length; index++) collapsed.add(segments.slice(0, index).join("/"));
  }
  let folder = String(preferred || "").includes("/") ? String(preferred).slice(0, String(preferred).lastIndexOf("/")) : "";
  while (folder) {
    collapsed.delete(folder);
    folder = folder.includes("/") ? folder.slice(0, folder.lastIndexOf("/")) : "";
  }
  return collapsed;
}
function expandSourceAncestors(path) {
  let folder = String(path || "").includes("/") ? String(path).slice(0, String(path).lastIndexOf("/")) : "";
  while (folder) {
    state.collapsedSourceFolders.delete(folder);
    folder = folder.includes("/") ? folder.slice(0, folder.lastIndexOf("/")) : "";
  }
}
async function loadCatalog(preferred = state.path) {
  const data = await responseJSON(await fetchWithTimeout("/_oxpio/sources"));
  state.catalogSources = data.sources;
  if (!state.sourceTreeInitialized) {
    state.collapsedSourceFolders = defaultCollapsedSourceFolders(data.sources, preferred);
    state.sourceTreeInitialized = true;
  } else {
    expandSourceAncestors(preferred);
  }
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
async function refreshCatalogFromUI() {
  if (state.busy || !confirmDiscard()) return;
  busy(true);
  const stopProgress = progressStatus("Refreshing documents…");
  try {
    await loadCatalog();
  } catch (error) {
    if (!error.loadSourceCancelled) status(isAbortError(error) ? "Document refresh cancelled." : `Document list failed: ${error.message}`);
  } finally {
    stopProgress();
    busy(false);
  }
}
async function refreshFilesFromUI() {
  if (state.busy) return;
  busy(true);
  const stopProgress = progressStatus("Refreshing files…");
  try {
    await refreshFiles();
  } catch (error) {
    status(isAbortError(error) ? "File refresh cancelled." : `File list failed: ${error.message}`);
  } finally {
    stopProgress();
    busy(false);
  }
}

async function refreshFiles(preferred = state.filePath) {
  const data = await responseJSON(await fetchWithTimeout("/_oxpio/files"));
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
    button.disabled = state.busy;
    button.dataset.path = entry.path;
    button.setAttribute("role", "treeitem");
    button.setAttribute("aria-selected", String(entry.path === preferred || entry.path === state.filePath));
    if (entry.kind === "folder") button.setAttribute("aria-expanded", String(!state.collapsedFolders.has(entry.path)));
    button.style.paddingLeft = `${0.35 + entry.path.split("/").length * 0.45}rem`;
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
        busy(true, "Loading folder…");
        refreshFiles(entry.path).catch(error => status(isAbortError(error) ? "Folder loading cancelled." : `File list failed: ${error.message}`)).finally(() => busy(false));
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
function fileSelectionSnapshot() {
  return {
    filePath: state.filePath,
    fileHash: state.fileHash,
    fileKind: state.fileKind,
    fileSourceKind: state.fileSourceKind,
    fileEditable: state.fileEditable,
    uploadFolder: $("#upload-folder").value,
  };
}
function restoreFileSelection(selection) {
  Object.assign(state, selection);
  $("#upload-folder").value = selection.uploadFolder;
  document.querySelectorAll(".file-entry").forEach(item => item.setAttribute("aria-selected", String(item.dataset.path === state.filePath)));
  updateFileActions();
}
function selectFileEntry(entry) {
  if (entry.sourceKind && entry.path !== state.path && !confirmDiscard()) return;
  const previousSelection = fileSelectionSnapshot();
  state.filePath = entry.path;
  state.fileHash = entry.hash;
  state.fileKind = entry.kind;
  state.fileSourceKind = entry.sourceKind || "";
  state.fileEditable = entry.editable !== false;
  $("#upload-folder").value = entry.kind === "folder" ? entry.path : (entry.path.includes("/") ? entry.path.slice(0, entry.path.lastIndexOf("/")) : "");
  document.querySelectorAll(".file-entry").forEach(item => item.setAttribute("aria-selected", String(item.dataset.path === entry.path)));
  updateFileActions();
  if (entry.sourceKind) loadSource(entry.path).catch(error => {
    restoreFileSelection(previousSelection);
    if (!isAbortError(error)) status(`Load failed: ${error.message}`);
  });
  else status(`Selected ${entry.path}`);
}
function updateFileActions() {
  const hasSelection = Boolean(state.filePath) && !state.busy;
  $("#file-rename").disabled = !hasSelection || !state.fileEditable;
  $("#file-delete").disabled = !hasSelection || !state.fileEditable;
}
function clearEditor() {
  cancelLivePreview();
  state.previewPending = "";
  state.path = "";
  state.kind = "article";
  state.source = "";
  state.baselineSource = "";
  state.hash = "";
  state.bodyBaseline = "";
  state.formBaseline = {};
  state.sourceChanged = false;
  state.previewURL = "";
  state.previewContentURL = "";
  state.previewPageURL = "";
  state.previewKind = "content";
  state.previewOpen = false;
  state.surface = "write";
  showForm({publish: false});
  setText("");
  updateMode();
  dirty(false);
  diagnostics();
  if (state.diffView) {
    state.diffView.destroy();
    state.diffView = null;
  }
  $("#diff-editor").hidden = true;
  $("#diff-wrap").hidden = true;
  state.previewResizeObserver?.disconnect();
  state.previewResizeObserver = null;
  $("#preview-frame").hidden = true;
  $("#preview-frame").removeAttribute("src");
  $("#preview-frame").style.height = "";
  $("#preview-empty").hidden = false;
  status("No Markdown sources");
}
async function loadSource(path) {
  cancelLivePreview();
  state.previewPending = "";
  const token = ++state.loadToken;
  let loadCancelled = false;
  let loadSucceeded = false;
  busy(true);
  try {
    const data = await responseJSON(await fetchWithTimeout(`/_oxpio/source-meta?path=${encodeURIComponent(path)}`));
    if (token !== state.loadToken) return;
    state.path = path;
    expandSourceAncestors(path);
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
    state.surface = state.mode === "source" ? "source" : "write";
    state.sourceChanged = false;
    state.previewURL = "";
    state.previewContentURL = "";
    state.previewPageURL = "";
    state.previewKind = "content";
    showForm(data.frontmatter);
    setText(state.mode === "source" ? data.source : data.body);
    state.bodyBaseline = text();
    updateMode();
    dirty(false);
    diagnostics();
    if (state.diffView) {
      state.diffView.destroy();
      state.diffView = null;
    }
    $("#diff-editor").hidden = true;
    $("#diff-wrap").hidden = true;
    state.previewResizeObserver?.disconnect();
    state.previewResizeObserver = null;
    $("#preview-frame").hidden = true;
    $("#preview-frame").removeAttribute("src");
    $("#preview-frame").style.height = "";
    $("#preview-empty").hidden = false;
    status(data.parseError ? `Fix this in source mode: ${data.parseError}` : `Loaded ${path}`);
    await offerDraft(path, state.hash, token);
    if (token !== state.loadToken) return;
    loadSucceeded = true;
  } catch (error) {
    if (isAbortError(error)) {
      loadCancelled = true;
      error.loadSourceCancelled = true;
      if (token === state.loadToken) {
        $("#source").value = state.path;
        renderSourceTree();
      }
      status("Document load cancelled.");
    } else {
      status(`Load failed: ${error.message}`);
    }
    throw error;
  }
  finally {
    if (token === state.loadToken && loadSucceeded && !loadCancelled) {
      if (state.surface === "write" && !state.suppressPreview) state.previewPending = "live";
      else if (state.keepPreview && state.previewOpen && !state.suppressPreview) state.previewPending = "full";
    }
    busy(false);
  }
}
function updateMode() {
  $("#metadata").hidden = state.mode === "source";
  const modeTabs = [$("#write-mode"), $("#source-mode"), $("#read-mode"), $("#diff-mode")];
  const activeTab = {write: $("#write-mode"), source: $("#source-mode"), read: $("#read-mode"), diff: $("#diff-mode")}[state.surface] || $("#write-mode");
  for (const tab of modeTabs) {
    const active = tab === activeTab;
    tab.setAttribute("aria-selected", String(active));
    tab.tabIndex = active ? 0 : -1;
  }
  $(".editor-preview-grid").dataset.mode = state.surface;
  $(".editor-preview-grid").setAttribute("aria-labelledby", activeTab?.id || "write-mode");
  $(".workspace").dataset.mode = state.surface;
  $("#editor-pane-title").textContent = state.surface === "write" ? "Write" : "Markdown";
  $("#preview-refresh").hidden = state.surface !== "read";
  const pagePreview = $("#page-preview");
  if (pagePreview) {
    pagePreview.hidden = state.surface !== "read";
    pagePreview.setAttribute("aria-pressed", String(state.previewKind === "page"));
  }
  if (state.surface !== "diff") $("#preview-title").textContent = state.surface === "write" ? "Write" : (state.previewKind === "page" ? "Page preview" : "Content preview");
}
function sourceBodyOffset(value) {
  const frontmatter = /^(?:\uFEFF)?---(?:\r\n|\r|\n)[\s\S]*?(?:\r\n|\r|\n)(?:---|\.\.\.)(?:(?:\r\n|\r|\n)|$)/.exec(value);
  return frontmatter ? value.slice(0, frontmatter[0].length).replace(/\r\n?/g, "\n").length : 0;
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
async function compose(signal) {
  signal = signal || state.operationController?.signal;
  if (state.mode === "source") return state.source;
  const changed = changes();
  const bodyChanged = text() !== state.bodyBaseline;
  if (!changed.length && !bodyChanged) return state.source;
  const request = {path: state.path, source: state.source};
  if (bodyChanged) request.body = text();
  if (changed.length) { request.fields = formValues(); request.changed = changed; }
  const result = await mutation("POST", "/_oxpio/frontmatter", request, {}, signal);
  return result.source;
}
async function operation(label, action) {
  if (state.busy || !state.path) return false;
  const controller = new AbortController();
  state.operationController = controller;
  state.operationOwned = false;
  state.operationCommitted = false;
  state.operationCancelled = false;
  busy(true, `${label} in progress… controls are temporarily disabled.`);
  try {
    await action();
    return true;
  } catch (error) {
    state.operationCancelled = error.name === "AbortError";
    if (state.operationCommitted) {
      if (label === "Save") saveState("saved", "Saved with warnings");
      diagnostics(error.diagnostics, `${label} committed, but refresh failed: ${error.message}`);
      status(isAbortError(error) ? `${label} committed; refresh cancelled.` : `${label} committed; refresh failed.`);
      return true;
    }
    if (state.operationCancelled) {
      if (label === "Save") saveState(state.dirty ? "unsaved" : "saved", "Save cancelled");
      status(`${label} cancelled.`);
    } else {
      diagnostics(error.diagnostics, `${label} failed: ${error.message}`);
      saveState("failed", label === "Save" ? "Build failed" : `${label} failed`);
      status(label === "Save" ? "Save failed; see diagnostics." : `${label} failed; see diagnostics.`);
    }
    return false;
  } finally {
    if (state.operationController === controller) state.operationController = null;
    busy(false);
  }
}
async function save() {
  await operation("Save", async () => {
    const stopProgress = progressStatus("Validating, building, and saving…");
    saveState("saving", "Saving");
    try {
      cancelLivePreview();
      state.previewPending = "";
      const path = state.path;
      const oldHash = state.hash;
      const mode = state.mode;
      const reopenLivePreview = state.surface === "write" && state.mode === "visual";
      const reopenPreview = state.keepPreview && state.surface !== "write" && state.previewOpen;
      const reopenPreviewKind = state.previewKind;
      let previewRefreshWarning = "";
      let previewRefreshError = null;
      const source = await compose();
      const result = await mutation("PUT", `/_oxpio/source?path=${encodeURIComponent(path)}`, source, {"X-OXPIO-Source-Hash": state.hash});
      markOperationCommitted("Save");
      await clearDraft(path);
      dirty(false);
      state.suppressPreview = true;
      try { await loadCatalog(path); } finally { state.suppressPreview = false; }
      if (mode === "source") { state.mode = "source"; setText(state.source); updateMode(); }
      if (reopenLivePreview) {
        state.livePreviewAfterCommit = true;
        state.previewPending = "live";
      } else if (reopenPreview) {
        let previewResult;
        let generatedPreviewToken = "";
        try {
          previewResult = await mutation("POST", "/_oxpio/preview", {path: state.path, source: state.source}, previewRequestHeaders());
          generatedPreviewToken = previewTokenFromURL(previewResult.contentURL || previewResult.url || previewResult.fullURL);
          diagnostics(previewResult.diagnostics);
          await applyPreviewResult(previewResult, reopenPreviewKind);
        } catch (error) {
          if (generatedPreviewToken) {
            await cleanupPreviewToken(generatedPreviewToken);
            if (state.previewToken === generatedPreviewToken) state.previewToken = "";
          }
          if (previewResult?.diagnostics && !error.diagnostics) error.diagnostics = previewResult.diagnostics;
          resetPreviewFrame();
          const empty = $("#preview-empty");
          empty.hidden = false;
          empty.classList.remove("is-loading");
          empty.textContent = isAbortError(error) ? "Preview refresh cancelled." : "Preview refresh failed; see diagnostics.";
          previewRefreshError = error;
          previewRefreshWarning = isAbortError(error) ? " Preview refresh cancelled." : " Preview refresh failed.";
        }
      }
      if (previewRefreshError) diagnostics([...(result.diagnostics || []), ...(previewRefreshError.diagnostics || [])], `Preview refresh failed: ${previewRefreshError.message}`);
      else diagnostics(result.diagnostics);
      const cleanupWarning = postCommitWarning(result);
      const saveWarning = result.warningCount || cleanupWarning || previewRefreshWarning;
      saveState("saved", saveWarning ? "Saved with warnings" : "Saved");
      status(saveWarning ? `Saved and rebuilt with warnings.${cleanupWarning}${previewRefreshWarning}` : "Saved and rebuilt.");
    } finally {
      stopProgress();
    }
  });
}
function showPreviewLoading(kind) {
  $("#diff-editor").hidden = true;
  $("#diff-wrap").hidden = true;
  $("#preview-frame").hidden = true;
  const empty = $("#preview-empty");
  empty.hidden = false;
  empty.classList.add("is-loading");
  empty.textContent = kind === "page" ? "Generating complete page preview…" : "Generating focused content preview…";
  $("#preview-title").textContent = kind === "page" ? "Page preview" : "Content preview";
  $("#preview-title-icon").setAttribute("href", "#icon-eye");
}

async function preview(kind = "content") {
  let generatedPreviewToken = "";
  if (!state.path) {
    $("#diff-editor").hidden = true;
    $("#preview-frame").hidden = true;
    const empty = $("#preview-empty");
    empty.hidden = false;
    empty.classList.remove("is-loading");
    empty.textContent = "No Markdown sources.";
    status("No Markdown sources");
    return;
  }
  showPreviewLoading(kind);
  const completed = await operation(kind === "page" ? "Page preview" : "Preview", async () => {
    status(kind === "page" ? "Generating complete page preview…" : "Generating focused content preview…");
    const result = await mutation("POST", "/_oxpio/preview", {path: state.path, source: await compose()}, previewRequestHeaders());
    generatedPreviewToken = previewTokenFromURL(result.contentURL || result.url || result.fullURL);
    diagnostics(result.diagnostics);
    try { await applyPreviewResult(result, kind); }
    catch (error) {
      error.diagnostics = [...(result.diagnostics || []), ...(error.diagnostics || [])];
      throw error;
    }
    const label = kind === "page" ? "Page preview" : "Content preview";
    status(result.warningCount ? `${label} generated with warnings.` : `${label} generated; source and public output were not changed.`);
  });
  if (!completed) {
    if (generatedPreviewToken) {
      await cleanupPreviewToken(generatedPreviewToken);
      if (state.previewToken === generatedPreviewToken) state.previewToken = "";
    }
    resetPreviewFrame();
    const empty = $("#preview-empty");
    empty.hidden = false;
    empty.classList.remove("is-loading");
    empty.textContent = state.operationCancelled ? "Preview cancelled." : "Preview failed; see diagnostics.";
    $("#preview-frame").hidden = true;
  }
}
async function pagePreview() { return preview("page"); }
function diffExtensions() {
  return [basicSetup, markdown(), ...(state.diffWrap ? [EditorView.lineWrapping] : [])];
}
function fitDiffView() {
  if (!state.diffView) return;
  const views = [state.diffView.a, state.diffView.b];
  const lineHeight = Math.max(...views.map(view => view.defaultLineHeight));
  const estimatedHeight = Math.max(30 * lineHeight, ...views.map(view => (view.state.doc.lines + 2) * lineHeight * 2));
  views.forEach(view => {
    view.dom.style.setProperty("height", `${estimatedHeight}px`, "important");
    view.scrollDOM.style.setProperty("height", `${estimatedHeight}px`, "important");
  });
  let attempts = 0;
  const measureFullDiff = () => {
    if (!state.diffView || !views.every(view => view.dom.isConnected)) return;
    views.forEach(view => {
      view.viewState.printing = true;
      view.viewState.mustMeasureContent = true;
      view.viewState.viewport = {from: 0, to: 0};
      view.measure();
      view.viewState.mustMeasureContent = true;
      view.viewState.viewport = {from: 0, to: 0};
      view.measure();
    });
    const complete = views.every(view => view.dom.querySelectorAll(".cm-content .cm-line").length >= view.state.doc.lines);
    if (!complete && attempts++ < 10) {
      setTimeout(measureFullDiff, 50);
      return;
    }
    const contentHeight = Math.max(...views.map(view => {
      const lines = view.dom.querySelectorAll(".cm-content .cm-line");
      const lastLine = lines[lines.length - 1];
      return lastLine ? lastLine.offsetTop + lastLine.offsetHeight + 8 : estimatedHeight;
    }));
    const height = Math.max(30 * lineHeight, contentHeight);
    views.forEach(view => {
      view.dom.style.setProperty("height", `${height}px`, "important");
      view.scrollDOM.style.setProperty("height", `${height}px`, "important");
    });
    state.diffView.measure();
  };
  setTimeout(measureFullDiff, 50);
}
function mountDiffView(parent, baseline, candidate) {
  state.diffView = new MergeView({
    a: {doc: baseline, extensions: diffExtensions()},
    b: {doc: candidate, extensions: diffExtensions()},
    parent,
  });
  fitDiffView();
}
function updateDiffControl() {
  const button = $("#diff-wrap");
  button.hidden = state.surface !== "diff" || !state.diffView;
  button.setAttribute("aria-pressed", String(state.diffWrap));
}
function showDiffLoading() {
  const host = $("#diff-editor");
  host.hidden = false;
  host.replaceChildren();
  const message = document.createElement("p");
  message.className = "preview-empty is-loading";
  message.textContent = "Generating diff…";
  host.append(message);
  $("#preview-frame").hidden = true;
  $("#preview-empty").hidden = true;
  $("#preview-title").textContent = "Diff";
  $("#preview-title-icon").setAttribute("href", "#icon-diff");
  $("#diff-wrap").hidden = true;
  if (state.diffView) {
    state.diffView.destroy();
    state.diffView = null;
  }
  updateDiffControl();
}

async function showDiff() {
  if (!state.path) {
    $("#diff-editor").hidden = true;
    $("#preview-frame").hidden = true;
    const empty = $("#preview-empty");
    empty.hidden = false;
    empty.classList.remove("is-loading");
    empty.textContent = "No Markdown sources.";
    status("No Markdown sources");
    return;
  }
  showDiffLoading();
  const completed = await operation("Diff", async () => {
    const candidate = await compose();
    const host = $("#diff-editor");
    state.previewOpen = false;
    state.previewResizeObserver?.disconnect();
    state.previewResizeObserver = null;
    host.replaceChildren();
    if (candidate === state.baselineSource) {
      host.textContent = "No changes";
      updateDiffControl();
      status("Diff generated; no changes.");
      return;
    }
    const heading = document.createElement("p");
    heading.className = "diff-summary";
    heading.append(`Frontmatter and body changes for ${state.path}`);
    const hint = document.createElement("span");
    hint.className = "diff-scroll-hint";
    hint.textContent = "Use horizontal scroll for long lines.";
    heading.append(hint);
    host.append(heading);
    const mergeHost = document.createElement("div");
    mergeHost.className = "merge-host";
    host.append(mergeHost);
    mountDiffView(mergeHost, state.baselineSource, candidate);
    updateDiffControl();
    status("Diff generated.");
  });
  if (!completed) {
    const host = $("#diff-editor");
    host.hidden = false;
    host.replaceChildren();
    const message = document.createElement("p");
    message.className = "preview-empty";
    message.textContent = state.operationCancelled ? "Diff cancelled." : "Diff failed; see diagnostics.";
    host.append(message);
    updateDiffControl();
  }
}
function resizePreviewFrame() {
  const frame = $("#preview-frame");
  const documentElement = frame.contentDocument?.documentElement;
  const body = frame.contentDocument?.body;
  if (!documentElement) return;
  frame.style.height = "0px";
  frame.style.height = `${Math.max(documentElement.scrollHeight, body?.scrollHeight || 0)}px`;
}
function bindPreviewFrame() {
  const frame = $("#preview-frame");
  if (!frame.contentWindow || !frame.contentDocument) return;
  frame.contentDocument.documentElement.dataset.oxpioEditorPreview = "";
  state.previewResizeObserver?.disconnect();
  resizePreviewFrame();
  if (window.ResizeObserver) {
    state.previewResizeObserver = new ResizeObserver(resizePreviewFrame);
    state.previewResizeObserver.observe(frame.contentDocument.documentElement);
  }
}
function destroyDiffView() {
  if (state.diffView) {
    state.diffView.destroy();
    state.diffView = null;
  }
  $("#diff-editor").hidden = true;
  $("#diff-wrap").hidden = true;
}
function showPreviewSurface() {
  $("#diff-editor").hidden = true;
  $("#diff-wrap").hidden = true;
  $("#preview-title").textContent = state.surface === "write" ? "Write" : (state.previewKind === "page" ? "Page preview" : "Content preview");
  $("#preview-title-icon").setAttribute("href", "#icon-eye");
  if (!$("#preview-frame").src) $("#preview-empty").hidden = false;
  else $("#preview-frame").hidden = false;
}
async function setWorkspaceMode(surface) {
  const wasDiff = state.surface === "diff";
  if (surface !== "write") {
    $("#media-panel").hidden = true;
  }
  if (surface === "source" && state.mode !== "source" && !(await toggleMode())) return;
  if (surface === "write" && state.mode !== "visual" && !(await toggleMode())) return;
  if (wasDiff && surface !== "diff") destroyDiffView();
  state.surface = surface;
  updateMode();
  if (surface === "read") {
    showPreviewSurface();
    await preview();
  } else if (surface === "diff") {
    await showDiff();
  } else {
    showPreviewSurface();
    if (surface === "write") scheduleLivePreview();
  }
}
function rebuildDiffView() {
  if (!state.diffView) return;
  const baseline = state.diffView.a.state.doc.toString();
  const candidate = state.diffView.b.state.doc.toString();
  const host = $(".merge-host");
  if (!host) return;
  state.diffView.destroy();
  host.replaceChildren();
  mountDiffView(host, baseline, candidate);
  updateDiffControl();
}

async function toggleMode(sourcePosition = null) {
  return operation("Switch mode", async () => {
    const source = await compose();
    if (state.mode === "visual") {
      state.source = source;
      state.sourceChanged = false;
      state.mode = "source";
      state.surface = "source";
      setText(source);
      if (sourcePosition != null) {
        const position = Math.max(0, Math.min(sourceBodyOffset(source) + sourcePosition, editor.state.doc.length));
        editor.dispatch({selection: {anchor: position}});
        editor.focus();
      }
    } else {
      const result = await mutation("POST", "/_oxpio/frontmatter", {path: state.path, source});
      if (result.parseError) throw new Error(result.parseError);
      state.source = source;
      state.mode = "visual";
      state.surface = "write";
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
const livePreviewLine = StateEffect.define();
const livePreviewRender = StateEffect.define();
class LivePreviewWidget extends WidgetType {
  constructor(text, className) { super(); this.text = text; this.className = className; }
  eq(other) { return this.text === other.text && this.className === other.className; }
  toDOM() {
    const element = document.createElement("span");
    element.className = `live-preview-token ${this.className}`;
    element.textContent = this.text;
    return element;
  }
}
class LiveRenderedWidget extends WidgetType {
  constructor(html, styles) { super(); this.html = html; this.styles = styles; }
  eq(other) { return this.html === other.html && this.styles === other.styles; }
  toDOM() {
    const element = document.createElement("div");
    element.className = "live-rendered-block";
    const shadow = element.attachShadow({mode: "open"});
    const style = document.createElement("style");
    style.textContent = `:host { --preview-bg: #fff; --preview-surface: #fff; --preview-text: #20252b; --preview-muted: #687687; --preview-accent: #1769aa; --preview-border: #dce1e6; --preview-code-bg: #20252b; --preview-code-text: #f5f7fa; --preview-inline-code: #eef1f4; --preview-table-head: #f0f4f7; --preview-callout: #f4f8fb; display: block; max-width: 100%; color: var(--preview-text); font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; line-height: 1.65; }${this.styles}`;
    const content = document.createElement("div");
    content.className = "content-preview-content";
    content.innerHTML = this.html;
    shadow.append(style, content);
    return element;
  }
}
function markdownBlocks(source) {
  const blocks = [];
  const separator = /\n[ \t]*\n+/g;
  let start = 0;
  let match;
  while ((match = separator.exec(source)) !== null) {
    if (match.index > start) blocks.push({from: start, to: match.index});
    start = match.index + match[0].length;
  }
  if (start < source.length) blocks.push({from: start, to: source.length});
  return blocks;
}
function serializeLivePreviewBlock(element, documentRoot) {
  const clone = element.cloneNode(true);
  for (const node of [clone, ...clone.querySelectorAll("[src], [href]")]) {
    for (const attribute of ["src", "href"]) {
      const value = node.getAttribute?.(attribute);
      if (value && !/^(?:#|data:|https?:|mailto:|javascript:)/iu.test(value)) node.setAttribute(attribute, new URL(value, documentRoot.baseURI).href);
    }
  }
  return clone.outerHTML;
}
function livePreviewStyles(documentRoot) {
  return [...(documentRoot?.styleSheets || [])].flatMap(sheet => {
    try { return [...sheet.cssRules].map(rule => rule.cssText); } catch { return []; }
  }).join("\n");
}
function liveReplace(from, to) { return from < to ? Decoration.replace({}).range(from, to) : null; }
function liveWidget(from, to, text, className) { return from < to ? Decoration.replace({widget: new LivePreviewWidget(text, className)}).range(from, to) : null; }
function renderedLivePreviewDecorations(viewState, _activeLine, rendered) {
  if (!rendered?.blocks?.length || rendered.body !== viewState.doc.toString()) return null;
  const blocks = markdownBlocks(rendered.body);
  if (blocks.length !== rendered.blocks.length) return null;
  const ranges = [];
  blocks.forEach((block, index) => {
    ranges.push(Decoration.replace({widget: new LiveRenderedWidget(rendered.blocks[index], rendered.styles), block: true}).range(block.from, block.to));
  });
  return ranges.length ? Decoration.set(ranges, true) : Decoration.none;
}
function buildLivePreviewDecorations(viewState, activeLine, rendered) {
  if (state.surface !== "write" || state.mode !== "visual") return Decoration.none;
  const renderedDecorations = renderedLivePreviewDecorations(viewState, activeLine, rendered);
  if (renderedDecorations) return renderedDecorations;
  const doc = viewState.doc;
  const ranges = [];
  for (let number = 1; number <= doc.lines; number++) {
    const line = doc.line(number);
    const source = line.text;
    const add = range => { if (range) ranges.push(range); };
    const heading = source.match(/^(\u0020{0,3})(#{1,6})\s+/);
    const blockquote = source.match(/^(\u0020{0,3}>\s?)/);
    const callout = source.match(/^(\u0020{0,3}>\s?)\[!([^\]]+)\]\s*/i);
    const list = source.match(/^(\u0020{0,3})(?:(?:[-+*])|(?:\d+[.)]))\s+/);
    const task = source.match(/^(\u0020{0,3})(?:[-+*]|\d+[.)])\s+\[[ xX]\]\s+/);
    const fence = source.match(/^\u0020{0,3}(`{3,}|~{3,})/);
    const tableRule = /^\s*\|?(?:\s*:?-+:?\s*\|)+\s*$/.test(source);
    const horizontalRule = /^\s{0,3}(?:\*\s*){3,}$|^\s{0,3}(?:-\s*){3,}$|^\s{0,3}(?:_\s*){3,}$/.test(source);
    if (heading) {
      ranges.push(Decoration.line({class: `live-heading live-heading-${heading[2].length}`}).range(line.from));
      add(liveReplace(line.from + heading[1].length, line.from + heading[0].length));
    } else if (task) {
      ranges.push(Decoration.line({class: "live-list live-task"}).range(line.from));
      add(liveReplace(line.from + task[1].length, line.from + task[0].length));
    } else if (list) {
      ranges.push(Decoration.line({class: "live-list"}).range(line.from));
      add(liveReplace(line.from + list[1].length, line.from + list[0].length));
    } else if (callout) {
      ranges.push(Decoration.line({class: "live-blockquote live-callout"}).range(line.from));
      add(liveReplace(line.from, line.from + callout[1].length));
      add(liveWidget(line.from + callout[1].length, line.from + callout[0].length, callout[2].toUpperCase(), "live-callout-label"));
    } else if (blockquote) {
      ranges.push(Decoration.line({class: "live-blockquote"}).range(line.from));
      add(liveReplace(line.from, line.from + blockquote[1].length));
    } else if (fence || tableRule || horizontalRule) {
      ranges.push(Decoration.line({class: fence ? "live-code-fence" : "live-hidden-line"}).range(line.from));
      add(liveReplace(line.from, line.to));
      continue;
    }
    const image = source.match(/!\[([^\]]*)\]\(([^)]+)\)/);
    const link = image || source.match(/\[([^\]]+)\]\(([^)]+)\)/);
    if (link) {
      const start = link.index;
      const label = image ? `🖼 ${link[1] || link[2]}` : link[1];
      add(liveWidget(line.from + start, line.from + start + link[0].length, label, image ? "live-image" : "live-link"));
      continue;
    }
    const markerStart = heading?.[0].length || task?.[0].length || list?.[0].length || 0;
    const markers = /(`+|\*\*|__|\*|_)/g;
    let marker;
    while ((marker = markers.exec(source)) !== null) {
      if (marker.index < markerStart) continue;
      add(liveReplace(line.from + marker.index, line.from + marker.index + marker[0].length));
    }
  }
  return ranges.length ? Decoration.set(ranges.sort((a, b) => a.from - b.from || a.to - b.to), true) : Decoration.none;
}
const livePreviewDecorations = StateField.define({
  create: viewState => {
    const rendered = {body: "", blocks: [], styles: ""};
    return {line: viewState.doc.lineAt(viewState.selection.main.head).number, rendered, decorations: buildLivePreviewDecorations(viewState, null, rendered)};
  },
  update(value, transaction) {
    let line = value.line;
    let rendered = value.rendered;
    let explicit = false;
    for (const effect of transaction.effects) {
      if (effect.is(livePreviewLine)) { line = effect.value; explicit = true; }
      if (effect.is(livePreviewRender)) { rendered = effect.value; explicit = true; }
    }
    if (transaction.docChanged || transaction.selection) line = transaction.state.doc.lineAt(transaction.state.selection.main.head).number;
    if (transaction.docChanged && rendered.body !== transaction.state.doc.toString()) rendered = {body: "", blocks: [], styles: ""};
    if (!transaction.docChanged && !transaction.selection && !explicit) return value;
    return {line, rendered, decorations: buildLivePreviewDecorations(transaction.state, line, rendered)};
  },
  provide: field => EditorView.decorations.from(field, value => value.decorations),
});
function editorExtensions() {
  return [basicSetup, markdown(), livePreviewDecorations, indentation.of(indentExtensions()), EditorView.lineWrapping, editable.of(EditorView.editable.of(!state.busy)),
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
        scheduleLivePreview();
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
function insertMedia(path, allowBusy = false) {
  if (state.busy && !allowBusy) return;
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
async function openMedia() {
  $("#media-panel").hidden = false;
  if (state.busy) return loadMedia(true);
  busy(true, "Loading media library…");
  try { await loadMedia(true); }
  catch (error) { status(isAbortError(error) ? "Media loading cancelled." : `Media library failed to load: ${error.message}`); }
  finally { busy(false); }
}
async function loadMedia(throwOnError = false) {
  const list = $("#media-list");
  list.textContent = "Loading…";
  try {
    const data = await responseJSON(await fetchWithTimeout("/_oxpio/media"));
    list.replaceChildren();
    if (!data.items.length) list.textContent = "No images yet.";
    for (const media of data.items) {
      const item = document.createElement("div"); item.className = "media-item";
      const image = document.createElement("img"); image.src = media.url; image.alt = media.path; image.loading = "lazy"; item.append(image);
      const addButton = (label, action) => { const button = document.createElement("button"); button.type = "button"; button.disabled = state.busy; button.textContent = label; button.onclick = () => { if (!state.busy) action(); }; item.append(button); };
      addButton(media.name, () => insertMedia(media.path));
      for (const [field, label] of [["cover", "Set cover"], ["banner", "Set banner"]]) {
        if (state.mode === "source" || (state.kind === "section" && field === "cover")) continue;
        addButton(label, () => { $(`[data-field="${field}"]`).value = media.path; dirty(); });
      }
      list.append(item);
    }
  } catch (error) {
    list.textContent = `Media library failed to load: ${error.message}`;
    if (throwOnError) throw error;
  }
}
async function uploadMedia(file) {
  if (!file || state.busy) return;
  if (file instanceof FileList && file.length !== 1) { status("Upload exactly one file."); return; }
  busy(true);
  const stopProgress = progressStatus("Upload: validating, building, and saving…");
  try {
    const form = new FormData();
    form.append("file", file);
    const folder = $("#upload-folder").value.trim() || (state.fileKind === "folder" ? state.filePath : "");
    if (folder) form.append("folder", folder);
    if (/\.md$/iu.test(file.name)) {
      const kind = $("#upload-kind").value;
      const isSectionFile = /^_index\.md$/iu.test(file.name);
      if (isSectionFile !== (kind === "section-index")) throw new Error(isSectionFile ? "Choose Section index for _index.md uploads." : "Choose Article for ordinary Markdown uploads.");
      form.append("kind", kind);
    }
    const result = await mutation("POST", "/_oxpio/media", form, {"X-OXPIO-File-Hash": "absent"});
    markOperationCommitted("Upload");
    await loadMedia(true);
    await refreshFiles(result.path);
    if (/\.md$/iu.test(file.name)) await loadCatalog(result.path);
    else {
      insertMedia(result.path, true);
      if (state.surface === "write" && state.mode === "visual") state.previewPending = "live";
    }
    status(`Uploaded ${result.path} and rebuilt successfully.${postCommitWarning(result)}`);
  } catch (error) {
    if (state.operationCommitted) {
      diagnostics(error.diagnostics, `Upload committed, but refresh failed: ${error.message}`);
      status(isAbortError(error) ? "Upload committed; refresh cancelled." : "Upload committed; refresh failed.");
    } else if (isAbortError(error)) status("Upload cancelled.");
    else { diagnostics(error.diagnostics, `Upload failed: ${error.message}`); saveState("failed", "Build failed"); status(`Upload failed: ${error.message}`); }
  }
  finally { stopProgress(); busy(false); $("#media-upload").value = ""; }
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
  const stopProgress = progressStatus(`${label}: validating, building, and saving…`);
  try {
    const result = await action();
    markOperationCommitted(label);
    if (reloadSource) {
      dirty(false);
      await loadCatalog(preferred);
    } else {
      await refreshFiles(preferred);
    }
    const rebuilt = result.warningCount !== undefined;
    status(`${label} completed${rebuilt ? " and rebuilt" : ""}.${postCommitWarning(result)}`);
  } catch (error) {
    if (state.operationCommitted) {
      diagnostics(error.diagnostics, `${label} committed, but refresh failed: ${error.message}`);
      status(isAbortError(error) ? `${label} committed; refresh cancelled.` : `${label} committed; refresh failed.`);
    } else if (isAbortError(error)) status(`${label} cancelled.`);
    else {
      diagnostics(error.diagnostics, `${label} failed: ${error.message}`);
      saveState("failed", `${label} failed`);
      status(`${label} failed; see diagnostics.`);
    }
    if (!isAbortError(error) && !state.operationCommitted) throw error;
  } finally {
    stopProgress();
    busy(false);
  }
}

document.querySelectorAll("button svg, label svg, .workspace-search svg").forEach(svg => { svg.setAttribute("aria-hidden", "true"); svg.setAttribute("focusable", "false"); });
editor = new EditorView({state: EditorState.create({extensions: editorExtensions()}), parent: $("#editor")});
editor.dom.addEventListener("click", event => {
  if (state.surface !== "write" || state.mode !== "visual") return;
  const target = event.target instanceof Element ? event.target.closest(".live-rendered-block") : null;
  if (!target) return;
  let position;
  try { position = editor.posAtDOM(target, 0); } catch { position = editor.state.selection.main.head; }
  event.preventDefault();
  toggleMode(position).catch(error => diagnostics(error.diagnostics, `Switch mode failed: ${error.message}`));
});
loadPreferences();
$("#document-search").oninput = filterDocuments;
$("#sidebar-toggle").onclick = event => { const open = $(".source-sidebar").classList.toggle("drawer-open"); event.currentTarget.setAttribute("aria-expanded", String(open)); event.currentTarget.setAttribute("aria-label", open ? "Close documents" : "Open documents"); };
document.addEventListener("keydown", event => { if (event.key === "Escape" && $(".source-sidebar").classList.contains("drawer-open")) { $(".source-sidebar").classList.remove("drawer-open"); $("#sidebar-toggle").setAttribute("aria-expanded", "false"); $("#sidebar-toggle").setAttribute("aria-label", "Open documents"); } });
$("#settings").onclick = () => $("#settings-dialog").showModal();
$("#cancel-operation").onclick = () => state.operationController ? state.operationController.abort() : cancelLivePreview(true);
$("#settings-save").onclick = storePreferences;
$("#metadata-toggle").onclick = event => {
  const collapsed = $("#metadata").classList.toggle("metadata-collapsed");
  const expanded = !collapsed;
  event.currentTarget.setAttribute("aria-expanded", String(expanded));
  event.currentTarget.setAttribute("aria-label", expanded ? "Hide metadata" : "Show metadata");
  event.currentTarget.title = expanded ? "Hide metadata" : "Show metadata";
  $("#metadata-toggle-icon").setAttribute("href", expanded ? "#icon-chevron-up" : "#icon-chevron-down");
};
$("#help-tooltip").onclick = event => {
  const open = event.currentTarget.getAttribute("aria-expanded") !== "true";
  event.currentTarget.setAttribute("aria-expanded", String(open));
  $("#help-tooltip-content").setAttribute("aria-hidden", String(!open));
};
document.addEventListener("click", event => {
  if (event.target.closest(".tooltip-anchor")) return;
  $("#help-tooltip").setAttribute("aria-expanded", "false");
  $("#help-tooltip-content").setAttribute("aria-hidden", "true");
});
document.addEventListener("keydown", event => {
  if (event.key !== "Escape") return;
  $("#help-tooltip").setAttribute("aria-expanded", "false");
  $("#help-tooltip-content").setAttribute("aria-hidden", "true");
});
$("#logout").onclick = async () => { try { await mutation("POST", "/_oxpio/logout"); location.href = "/_oxpio/login"; } catch (error) { status(`Log out failed: ${error.message}`); } };
$("#write-mode").onclick = () => setWorkspaceMode("write").catch(error => diagnostics(error.diagnostics, `Write mode failed: ${error.message}`));
$("#source-mode").onclick = () => setWorkspaceMode("source").catch(error => diagnostics(error.diagnostics, `Source mode failed: ${error.message}`));
$("#read-mode").onclick = () => setWorkspaceMode("read").catch(error => diagnostics(error.diagnostics, `Reading mode failed: ${error.message}`));
$("#diff-mode").onclick = () => setWorkspaceMode("diff").catch(error => diagnostics(error.diagnostics, `Diff failed: ${error.message}`));
$(".editor-tabs").onkeydown = event => {
  if (![
    "ArrowLeft", "ArrowRight", "Home", "End",
  ].includes(event.key)) return;
  const tabs = [...document.querySelectorAll(".editor-tabs [role=tab]")];
  const current = tabs.indexOf(document.activeElement);
  if (current < 0) return;
  event.preventDefault();
  const next = event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : (current + (event.key === "ArrowLeft" ? -1 : 1) + tabs.length) % tabs.length;
  tabs[next].focus();
  tabs[next].click();
};
$("#preview-refresh").onclick = () => preview("content");
$("#page-preview").onclick = pagePreview;
$("#source").onchange = event => {
  if (confirmDiscard()) loadSource(event.target.value).catch(error => { if (!isAbortError(error)) status(`Load failed: ${error.message}`); });
  else event.target.value = state.path;
};
$("#refresh").onclick = refreshCatalogFromUI;
$("#save").onclick = save;
$("#diff-wrap").onclick = () => { state.diffWrap = !state.diffWrap; rebuildDiffView(); };
$("#toolbar-fullscreen").onclick = event => {
  const fullscreen = document.body.classList.toggle("editor-fullscreen");
  event.currentTarget.setAttribute("aria-pressed", String(fullscreen));
};
$("#metadata").oninput = () => { dirty(); scheduleLivePreview(); };
$("#metadata").onsubmit = event => event.preventDefault();
$("#file-refresh").onclick = refreshFilesFromUI;
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
    const result = await mutation("DELETE", `/_oxpio/file?path=${encodeURIComponent(pathValue)}&confirm=true`, null, {"X-OXPIO-File-Hash": fileHash});
    await clearDraft(pathValue);
    return result;
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
      await runFileMutation("Create folder", () => mutation("POST", "/_oxpio/file/folder", {path: pathValue}, {"X-OXPIO-File-Hash": "absent"}), pathValue, false);
    } else if (mode === "markdown") {
      const request = {path: pathValue, title: $("#file-title").value, type: $("#file-type").value, date: $("#file-date").value, kind: $("#file-kind").value};
      await runFileMutation("Create Markdown file", () => mutation("POST", "/_oxpio/file/markdown", request, {"X-OXPIO-File-Hash": "absent"}), pathValue);
    } else if (mode === "rename") {
      const oldPath = $("#file-original").value;
      const affectsCurrent = pathContains(oldPath, state.path);
      if (affectsCurrent && !confirmDiscard()) return;
      const suffix = affectsCurrent ? state.path.slice(oldPath.length) : "";
      const preferred = affectsCurrent ? pathValue + suffix : state.path;
      await runFileMutation("Rename", () => mutation("PUT", `/_oxpio/file?path=${encodeURIComponent(oldPath)}`, {destination: pathValue}, {"X-OXPIO-File-Hash": state.fileHash}), preferred, affectsCurrent);
    }
    dialog.close();
  } catch (error) {
    if (isAbortError(error)) status(`${mode === "rename" ? "Rename" : "File operation"} cancelled.`);
    else {
      diagnostics(error.diagnostics, error.message);
      saveState("failed", "Build failed");
      status(`${mode === "rename" ? "Rename" : "File operation"} failed: ${error.message}`);
    }
  }
};
$("#new").onclick = () => { if (confirmDiscard()) $("#new-dialog").showModal(); };
$("#new-cancel").onclick = () => { $("#new-dialog").close(); $("#new").focus(); };
$("#new-form").onsubmit = async event => {
  if (event.submitter?.value === "cancel") {
    event.preventDefault();
    $("#new-dialog").close();
    $("#new").focus();
    return;
  }
  event.preventDefault();
  if (state.busy) return;
  const form = new URLSearchParams(new FormData(event.target));
  busy(true);
  const stopProgress = progressStatus("Create: validating, building, and saving…");
  try {
    const result = await mutation("POST", "/_oxpio/source", form, {"X-OXPIO-Source-Hash": "absent"});
    markOperationCommitted("Create");
    $("#new-dialog").close(); dirty(false);
    await loadCatalog(form.get("path"));
    status(`Created and rebuilt.${postCommitWarning(result)}`);
  } catch (error) {
    if (state.operationCommitted) {
      diagnostics(error.diagnostics, `Create committed, but refresh failed: ${error.message}`);
      status(isAbortError(error) ? "Create committed; refresh cancelled." : "Create committed; refresh failed.");
    } else if (isAbortError(error)) status("Create cancelled.");
    else { diagnostics(error.diagnostics, error.message); saveState("failed", "Build failed"); status(`Create failed: ${error.message}`); }
    $("#new-dialog").close();
  }
  finally { stopProgress(); busy(false); }
};
$("#delete").onclick = () => {
  if (state.kind !== "article" || !confirm("Delete this article?")) return;
  operation("Delete", async () => {
    const stopProgress = progressStatus("Delete: validating, building, and saving…");
    try {
      const deletedPath = state.path;
      const result = await mutation("DELETE", `/_oxpio/source?path=${encodeURIComponent(deletedPath)}&confirm=true`, null, {"X-OXPIO-Source-Hash": state.hash});
      markOperationCommitted("Delete");
      await clearDraft(deletedPath);
      dirty(false); await loadCatalog(""); status(`Deleted and rebuilt.${postCommitWarning(result)}`);
    } finally {
      stopProgress();
    }
  });
};
$("#media").onclick = openMedia;
$("#media-close").onclick = () => { $("#media-panel").hidden = true; };
$("#media-upload").onchange = event => { if (event.target.files.length !== 1) { saveState("failed", "Upload failed"); diagnostics([], "Upload exactly one file; no files were written."); status("Upload exactly one file; no files were written."); event.target.value = ""; return; } uploadMedia(event.target.files[0]); };
const mediaDropzone = $("#media-dropzone");
["dragenter", "dragover"].forEach(type => mediaDropzone.addEventListener(type, event => { event.preventDefault(); mediaDropzone.classList.add("drag-over"); }));
["dragleave", "drop"].forEach(type => mediaDropzone.addEventListener(type, event => { event.preventDefault(); mediaDropzone.classList.remove("drag-over"); }));
mediaDropzone.addEventListener("drop", event => {
  const files = [...(event.dataTransfer?.files || [])];
  if (files.length !== 1) { saveState("failed", "Upload failed"); diagnostics([], "Upload exactly one file; no files were written."); status("Upload exactly one file; no files were written."); return; }
  uploadMedia(files[0]);
});
document.querySelectorAll("[data-command]").forEach(button => {
  button.onmousedown = event => event.preventDefault();
  button.onclick = () => command(button.dataset.command);
});
window.addEventListener("beforeunload", event => { if (state.dirty) { event.preventDefault(); event.returnValue = ""; } });
loadCatalog().catch(error => { if (!error.loadSourceCancelled) status(isAbortError(error) ? "Document load cancelled." : `Editor unavailable: ${error.message}`); });
