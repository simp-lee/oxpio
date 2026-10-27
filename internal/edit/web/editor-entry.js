import {basicSetup} from "codemirror";
import {markdown} from "@codemirror/lang-markdown";
import {GFM} from "@lezer/markdown";
import {indentUnit, syntaxTree} from "@codemirror/language";
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
  fileDialogMode: "", dirty: false, busy: false, busyCount: 0, busyReason: "", operationController: null, operationOwned: false, operationCommitted: false, operationCancelled: false, loadToken: 0,
  draftTimer: 0, diffView: null, diffWrap: false, previewURL: "", previewContentURL: "", previewPageURL: "", previewKind: "content", previewResizeObserver: null, fileEditable: false, fileEntries: null, indent: 2, keepPreview: true, previewOpen: false, suppressPreview: false, previewPending: "", previewToken: "", catalogSources: [], sourceTreeInitialized: false, collapsedFolders: new Set(), collapsedSourceFolders: new Set(),
};
const editable = new Compartment();
let editor;
let suppressChanges = false;
const requestTimeout = 30000;

function status(message) {
  const target = $("#status");
  target.textContent = message;
  target.dataset.visibility = /^(?:Loading|Loaded)\b/u.test(message) ? "quiet" : message ? "visible" : "empty";
}
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
  const icon = $("#save-state-icon");
  const iconByState = {saved: "#icon-check-circle", unsaved: "#icon-circle-dot", draft: "#icon-circle-dot", saving: "#icon-loader", failed: "#icon-alert-circle"};
  if (target) target.dataset.state = value;
  if (icon) icon.setAttribute("href", iconByState[value] || "#icon-circle-dot");
  const targetLabel = $("#save-state-label");
  if (targetLabel) targetLabel.textContent = value === "draft" ? "Unsaved" : (label || value);
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
function disposeHighCostWorker() {
  if (highCostWorker) highCostWorker.terminate();
  highCostWorker = null;
  if (highCostWorkerURL) URL.revokeObjectURL(highCostWorkerURL);
  highCostWorkerURL = "";
}
function cancelLivePreview(notify = false) {
  if (highCostFrame) cancelAnimationFrame(highCostFrame);
  highCostFrame = 0;
  highCostPendingSource = "";
  disposeHighCostWorker();
  highCostGeneration += 1;
  highCostInFlight = false;
  if (notify) status("Live preview cancelled.");
}
function scheduleLivePreview() {
  if (state.surface !== "write" || state.mode !== "visual" || !editor) {
    cancelLivePreview();
    return;
  }
  diagnostics();
  scheduleHighCostTasks(editor.state);
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
const inlineNodeNames = new Set(["StrongEmphasis", "Emphasis", "Strikethrough", "Link", "Image", "InlineCode", "Autolink", "Subscript", "Superscript", "Emoji"]);
const renderedInlineSelectors = {StrongEmphasis: "strong", Emphasis: "em", Strikethrough: "del", Link: "a", Autolink: "a", InlineCode: "code", Image: "img"};
const markerNodeNames = new Set(["EmphasisMark", "StrikethroughMark", "LinkMark", "CodeMark", "URL", "HeaderMark", "ListMark", "QuoteMark", "TableDelimiter", "TaskMarker"]);
const livePreviewMetrics = {coreRenders: 0, workerTasks: 0, workerCancels: 0, serviceRequests: 0, coreRenderMs: [], keyToPaintMs: []};
window.__oxpioLivePreviewMetrics = livePreviewMetrics;
let highCostWorker;
let highCostWorkerURL = "";
let highCostGeneration = 0;
let highCostFrame = 0;
let highCostPendingSource = "";
let mathRuntimePromise = null;
let mermaidRuntimePromise = null;
let mermaidRenderID = 0;
let highCostInFlight = false;
let renderGeneration = 0;
function ensureMathRuntime() {
  if (window.katex) return Promise.resolve(window.katex);
  if (mathRuntimePromise) return mathRuntimePromise;
  mathRuntimePromise = new Promise((resolve, reject) => {
    const load = (url, next) => {
      const script = document.createElement("script");
      script.src = new URL(url, document.baseURI).href;
      script.onload = next;
      script.onerror = () => reject(new Error(`${url} failed to load`));
      document.head.append(script);
    };
    load("editor-runtime/katex.min.js", () => load("editor-runtime/mhchem.min.js", () => resolve(window.katex)));
  });
  return mathRuntimePromise;
}
function ensureMermaidRuntime() {
  if (window.mermaid) return Promise.resolve(window.mermaid);
  if (mermaidRuntimePromise) return mermaidRuntimePromise;
  mermaidRuntimePromise = new Promise((resolve, reject) => {
    const script = document.createElement("script");
    script.src = new URL("editor-runtime/mermaid.min.js", document.baseURI).href;
    script.onload = () => resolve(window.mermaid);
    script.onerror = () => reject(new Error("Mermaid runtime failed to load"));
    document.head.append(script);
  });
  return mermaidRuntimePromise;
}
function directBlockNodes(viewState) {
  const nodes = [];
  for (let node = syntaxTree(viewState).topNode.firstChild; node; node = node.nextSibling) {
    if (node.to > node.from) nodes.push(node);
  }
  return nodes;
}
function emptyRenderDocument(viewState) {
  return {body: viewState.doc.toString(), source: viewState.doc.toString(), blocks: [], styles: "", generation: ++renderGeneration};
}
function preserveRenderedDocument(rendered, transaction) {
  const source = transaction.state.doc.toString();
  if (!rendered?.blocks?.length) return rendered ? {...rendered, body: source, source} : emptyRenderDocument(transaction.state);
  const changed = [];
  transaction.changes.iterChangedRanges((from, to) => changed.push({from, to}));
  const oldSource = rendered.source || rendered.body || "";
  const intersects = (block, range) => {
    const from = Math.max(0, range.from - 1);
    const to = Math.min(oldSource.length, range.to + 1);
    return from <= block.to && to >= block.from;
  };
  const blocks = rendered.blocks.filter(block => !changed.some(range => intersects(block, range))).map(block => {
    const map = (value, assoc = 1) => transaction.changes.mapPos(value, assoc);
    return {...block, from: map(block.from), to: map(block.to, -1), html: remapRenderedMathOffsets(block.html, oldSource, source, transaction.changes), inline: (block.inline || []).map(range => ({...range, from: map(range.from), to: map(range.to, -1)}))};
  }).filter(block => block.from < block.to);
  return {...rendered, body: source, source, blocks};
}
function findActiveSyntaxNode(viewState, position) {
  let node = syntaxTree(viewState).resolveInner(Math.max(0, position), -1);
  while (node && node.name !== "Document") {
    if (inlineNodeNames.has(node.name) || node.name === "Paragraph" || node.name === "TableCell" || node.name === "Table" || /^ATXHeading[1-6]$/u.test(node.name) || node.name.endsWith("List") || node.name === "ListItem" || node.name === "Blockquote" || node.name === "FencedCode" || node.name === "CodeBlock") return {from: node.from, to: node.to, name: node.name};
    node = node.parent;
  }
  return null;
}
function activeNodeMatches(node, active) { return !!active && node.from === active.from && node.to === active.to && node.name === active.name; }
function renderedInlineHTML(block, node) {
  const selector = renderedInlineSelectors[node.name];
  if (!selector || !block?.html) return "";
  const root = document.createElement("div");
  root.innerHTML = block.html;
  const elements = [...root.querySelectorAll("strong, em, del, a, code, img")];
  let elementIndex = 0;
  for (const range of block.inline || []) {
    const rangeSelector = renderedInlineSelectors[range.name];
    if (!rangeSelector) continue;
    const index = elements.findIndex((element, index) => index >= elementIndex && element.matches(rangeSelector));
    if (index < 0) continue;
    if (range.from === node.from && range.to === node.to && range.name === node.name) return elements[index].outerHTML;
    elementIndex = index + 1;
  }
  return "";
}
function activeSourceDecorations(viewState, active, renderedBlock = null, renderedStyles = "") {
  const ranges = [];
  const activeBlock = directBlockNodes(viewState).find(node => active && node.from <= active.from && node.to >= active.to);
  if (activeBlock) {
    const line = viewState.doc.lineAt(activeBlock.from);
    const heading = /^ATXHeading[1-6]$/u.test(activeBlock.name);
    if (heading) ranges.push(Decoration.line({class: `live-heading live-heading-${activeBlock.name.slice(-1)}`}).range(line.from));
  }
  const nodes = [];
  syntaxTree(viewState).iterate({enter(node) {
    const inActiveBlock = !!activeBlock && node.from >= activeBlock.from && node.to <= activeBlock.to;
    if (inActiveBlock && inlineNodeNames.has(node.name)) nodes.push({from: node.from, to: node.to, name: node.name});
    if (inActiveBlock && new Set(["HeaderMark", "ListMark", "QuoteMark", "TableDelimiter"]).has(node.name) && activeBlock.name !== "FencedCode") {
      const activeBlockNode = active && active.name === activeBlock.name && active.from === activeBlock.from && active.to === activeBlock.to;
      if (!activeBlockNode && (!active || node.from < active.from || node.to > active.to)) {
        const range = liveReplace(node.from, node.to);
        if (range) ranges.push(range);
      }
    }
  }});
  const covered = [];
  for (const node of nodes.sort((a, b) => a.from - b.from || b.to - a.to)) {
    if (covered.some(range => node.from >= range.from && node.to <= range.to)) continue;
    const containsActive = !!active && node.from <= active.from && node.to >= active.to;
    if (!activeNodeMatches(node, active) && !containsActive && renderedBlock) {
      const html = renderedInlineHTML(renderedBlock, node);
      if (html) {
        ranges.push(Decoration.replace({widget: new LiveRenderedWidget(html, renderedStyles, node.from, node.to, node.name, viewState.doc.toString(), [], true)}).range(node.from, node.to));
      } else {
        ranges.push(...liveNodeMarkerRanges(viewState, node, active));
      }
    } else if (!activeNodeMatches(node, active)) {
      ranges.push(...liveNodeMarkerRanges(viewState, node, active));
    }
    covered.push({from: node.from, to: node.to});
  }
  return ranges;
}
function utf8BoundaryMap(source) {
  const map = new Map([[0, 0]]);
  const encoder = new TextEncoder();
  let bytes = 0;
  let units = 0;
  for (const character of source) {
    bytes += encoder.encode(character).length;
    units += character.length;
    map.set(bytes, units);
  }
  return map;
}
function utf8OffsetForCodeUnits(source, target) {
  const encoder = new TextEncoder();
  let bytes = 0;
  let units = 0;
  for (const character of source) {
    if (units >= target) break;
    bytes += encoder.encode(character).length;
    units += character.length;
  }
  return bytes;
}
function remapRenderedMathOffsets(html, oldSource, newSource, changes) {
  if (!html || !html.includes("data-live-math-")) return html;
  const root = document.createElement("div");
  root.innerHTML = html;
  const oldBoundaries = utf8BoundaryMap(oldSource);
  root.querySelectorAll("[data-live-math-from][data-live-math-to]").forEach(math => {
    const oldFrom = oldBoundaries.get(Number(math.getAttribute("data-live-math-from")));
    const oldTo = oldBoundaries.get(Number(math.getAttribute("data-live-math-to")));
    if (oldFrom === undefined || oldTo === undefined) return;
    const from = changes.mapPos(oldFrom, 1);
    const to = changes.mapPos(oldTo, -1);
    math.setAttribute("data-live-math-from", String(utf8OffsetForCodeUnits(newSource, from)));
    math.setAttribute("data-live-math-to", String(utf8OffsetForCodeUnits(newSource, to)));
  });
  return root.innerHTML;
}
function codeMirrorInlineRanges(from, to) {
  if (!editor) return [];
  const ranges = [];
  syntaxTree(editor.state).iterate({enter(node) {
    if (node.from >= from && node.to <= to && inlineNodeNames.has(node.name)) ranges.push({from: node.from, to: node.to, name: node.name});
  }});
  return ranges;
}
function normalizeSharedRender(result) {
  if (!result || result.error) throw new Error(result?.error || "Markdown core returned no result");
  const source = result.source || "";
  const boundaries = utf8BoundaryMap(source);
  const offset = value => boundaries.get(Number(value)) ?? 0;
  return {
    body: source,
    source,
    styles: "",
    generation: ++renderGeneration,
    blocks: (result.blocks || []).map(block => {
      let from = offset(block.from);
      let to = offset(block.to);
      const syntaxKinds = {FencedCodeBlock: "FencedCode", Callout: "Blockquote", Blockquote: "Blockquote", Table: "Table", HTMLBlock: "CommentBlock", List: "BulletList"};
      const container = directBlockNodes(editor.state).find(node => node.from <= from && node.to > from && (node.name === syntaxKinds[block.kind] || (block.kind === "List" && node.name === "OrderedList")));
      if (container) {
        from = container.from;
        to = container.to;
      }
      return {from, to, name: block.kind, html: block.html, inline: codeMirrorInlineRanges(from, to)};
    }),
  };
}
function ensureHighCostWorker() {
  if (highCostWorker || typeof Worker === "undefined") return highCostWorker;
  const wasmURL = JSON.stringify(new URL("editor-runtime/live-preview.wasm", document.baseURI).href);
  const execURL = JSON.stringify(new URL("editor-runtime/wasm_exec.js", document.baseURI).href);
  const workerSource = `importScripts(${execURL}); const go = new Go(); let cancelledBefore = 0; const ready = (async () => { const response = await fetch(${wasmURL}); if (!response.ok) throw new Error("Markdown WASM request failed: " + response.status); const bytes = await response.arrayBuffer(); const compiled = await WebAssembly.instantiate(bytes, go.importObject); go.run(compiled.instance); while (typeof self.oxpioLivePreviewRender !== "function") await new Promise(resolve => setTimeout(resolve, 0)); })(); self.onmessage = async event => { if (event.data.cancelBefore) { cancelledBefore = Math.max(cancelledBefore, event.data.cancelBefore); return; } const generation = event.data.generation; if (generation <= cancelledBefore) return; try { await ready; if (generation <= cancelledBefore) return; const result = JSON.parse(self.oxpioLivePreviewRender(event.data.source)); if (generation > cancelledBefore) self.postMessage({generation, result}); } catch (error) { self.postMessage({generation, error: error.message || String(error)}); } };`;
  highCostWorkerURL = URL.createObjectURL(new Blob([workerSource], {type: "text/javascript"}));
  highCostWorker = new Worker(highCostWorkerURL);
  highCostWorker.onmessage = event => {
    if (event.data.generation !== highCostGeneration) return;
    highCostInFlight = false;
    try {
      if (event.data.error) throw new Error(event.data.error);
      const started = performance.now();
      const rendered = normalizeSharedRender(event.data.result);
      livePreviewMetrics.coreRenders += 1;
      livePreviewMetrics.coreRenderMs.push(performance.now() - started);
      if (livePreviewMetrics.coreRenderMs.length > 200) livePreviewMetrics.coreRenderMs.shift();
      if (editor && rendered.body === editor.state.doc.toString()) editor.dispatch({effects: livePreviewCore.of(rendered)});
    } catch (error) {
      diagnostics([], `Write rendering failed: ${error.message}`);
      status(`Write rendering failed: ${error.message}`);
    }
  };
  highCostWorker.onerror = event => {
    highCostInFlight = false;
    diagnostics([], "Write rendering Worker failed.");
    status("Write rendering Worker failed.");
    disposeHighCostWorker();
  };
  return highCostWorker;
}
function startHighCostTask(source) {
  if (source.includes("$") || source.includes("\\(")) ensureMathRuntime().catch(() => {});
  const worker = ensureHighCostWorker();
  if (!worker) return;
  if (highCostInFlight) {
    livePreviewMetrics.workerCancels += 1;
    worker.postMessage({cancelBefore: highCostGeneration});
  }
  highCostGeneration += 1;
  highCostInFlight = true;
  livePreviewMetrics.workerTasks += 1;
  worker.postMessage({generation: highCostGeneration, source});
}
function scheduleHighCostTasks(viewState) {
  highCostPendingSource = viewState.doc.toString();
  if (highCostFrame) return;
  highCostFrame = requestAnimationFrame(() => {
    highCostFrame = 0;
    const source = highCostPendingSource;
    highCostPendingSource = "";
    if (source) startHighCostTask(source);
  });
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
  const active = state.busy;
  indicator.hidden = !active;
  if (active) {
    $("#busy-message").textContent = state.busyReason;
    $("#cancel-operation").hidden = !state.operationController;
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
  if (!window.indexedDB || !current()) return false;
  const exact = await draftRecord(`${path}:${hash}`);
  if (!current()) return false;
  let record = exact;
  if (!record) {
    const db = await openDraftDB();
    if (!db) return false;
    record = await new Promise((resolve, reject) => {
      const request = db.transaction("drafts", "readonly").objectStore("drafts").getAll();
      request.onsuccess = () => resolve(request.result.filter(item => item.path === path).sort((a, b) => b.savedAt - a.savedAt)[0] || null);
      request.onerror = () => reject(request.error);
    });
  }
  if (!record || !record.candidate || !current()) return false;
  if (confirm(`Restore the unsaved local draft for ${path}?`)) {
    if (!current()) return false;
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
    return true;
  }
  await clearDraftKey(record.key);
  return false;
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
    syncRenderedTheme();
    $("#preference-theme").value = preferences.theme || "system";
    $("#preference-indent").value = state.indent;
    $("#preference-preview").checked = state.keepPreview;
    applyIndentPreference();
  } catch { /* browser preferences are best effort and never enter the vault */ }
}
function syncRenderedTheme() {
  const theme = document.documentElement.dataset.theme || "";
  document.querySelectorAll(".live-rendered-block, .live-rendered-inline").forEach(element => {
    element.dataset.theme = theme || "system";
  });
}
function storePreferences() {
  const preferences = {theme: $("#preference-theme").value, indent: Number($("#preference-indent").value) || 2, keepPreview: $("#preference-preview").checked};
  state.indent = Math.max(1, Math.min(8, preferences.indent));
  state.keepPreview = preferences.keepPreview;
  localStorage.setItem("oxpio-editor-preferences", JSON.stringify({...preferences, indent: state.indent}));
  document.documentElement.dataset.theme = preferences.theme === "system" ? "" : preferences.theme;
  document.documentElement.style.colorScheme = preferences.theme === "dark" ? "dark" : preferences.theme === "light" ? "light" : "";
  syncRenderedTheme();
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
  if (String(url).endsWith("/_oxpio/preview")) livePreviewMetrics.serviceRequests += 1;
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
    const sourceIcon = source.kind === "section" ? "folder" : (/\.md$/iu.test(source.relPath) ? "file" : "code");
    icon.innerHTML = `<svg aria-hidden="true"><use href="#icon-${sourceIcon}"></use></svg>`;
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
  const initialLoad = !state.sourceTreeInitialized;
  let stopProgress = null;
  if (initialLoad) {
    busy(true, "Loading document catalog…");
    stopProgress = progressStatus("Loading document catalog…");
  }
  try {
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
    const defaultPath = data.sources.find(source => source.relPath === "_index.md")?.relPath || data.sources[0]?.relPath || "";
    select.value = data.sources.some(source => source.relPath === preferred) ? preferred : defaultPath;
    const loadResult = select.value ? await loadSource(select.value, initialLoad ? "Loading document catalog…" : undefined) : {draftRestored: false, parseError: ""};
    if (!select.value) clearEditor();
    await refreshFiles(preferred);
    status(loadResult.parseError ? `Fix this in source mode: ${loadResult.parseError}` : (loadResult.draftRestored ? "Local draft restored; save to publish it." : (select.value ? `Loaded ${select.value}` : "Select a Markdown document to begin editing.")));
  } finally {
    if (initialLoad) {
      stopProgress?.();
      busy(false);
    }
  }
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
  let data;
  try {
    data = await responseJSON(await fetchWithTimeout("/_oxpio/files"));
  } catch (error) {
    state.fileEntries = null;
    throw error;
  }
  state.fileEntries = data.entries;
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
    button.style.setProperty("--file-indent", `${0.35 + entry.path.split("/").length * 0.45}rem`);
    const icon = document.createElement("span");
    icon.className = "file-entry-icon";
    const fileIcon = entry.kind === "folder" ? "folder" : (/\.(png|jpe?g|webp|svg)$/iu.test(entry.path) ? "image" : (/\.md$/iu.test(entry.path) ? "file" : "code"));
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
  updateUploadGuidance();
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
  updateUploadGuidance();
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
  status("Select a Markdown document to begin editing.");
}
async function loadSource(path, busyReason = "Working… controls are temporarily disabled.") {
  cancelLivePreview();
  state.previewPending = "";
  const token = ++state.loadToken;
  let loadCancelled = false;
  let loadSucceeded = false;
  busy(true, busyReason);
  try {
    const data = await responseJSON(await fetchWithTimeout(`/_oxpio/source-meta?path=${encodeURIComponent(path)}`));
    if (token !== state.loadToken) return {draftRestored: false, parseError: ""};
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
    const draftRestored = await offerDraft(path, state.hash, token);
    if (token !== state.loadToken) return false;
    loadSucceeded = true;
    return {draftRestored, parseError: data.parseError || ""};
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
const livePreviewActive = StateEffect.define();
const livePreviewCore = StateEffect.define();
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
function liveInlineRangesEqual(left, right) {
  if (left === right) return true;
  if (!left || !right || left.length !== right.length) return false;
  return left.every((range, index) => {
    const other = right[index];
    return range.from === other.from && range.to === other.to && range.name === other.name;
  });
}
class LiveRenderedWidget extends WidgetType {
  constructor(html, styles, from = 0, to = 0, name = "", source = "", inline = [], inlineHost = false) { super(); this.html = html; this.styles = styles; this.from = from; this.to = to; this.name = name; this.source = source; this.inline = inline; this.inlineHost = inlineHost; }
  eq(other) { return this.html === other.html && this.styles === other.styles && this.from === other.from && this.to === other.to && this.name === other.name && liveInlineRangesEqual(this.inline, other.inline) && this.inlineHost === other.inlineHost; }
  toDOM() {
    const element = document.createElement("div");
    element.className = this.inlineHost ? "live-rendered-inline" : "live-rendered-block";
    element.dataset.theme = document.documentElement.dataset.theme || "system";
    element.dataset.liveBlockFrom = String(this.from);
    element.dataset.liveBlockTo = String(this.to);
    element.dataset.liveBlockName = this.name;
    if (this.inlineHost) {
      element.dataset.liveInlineFrom = String(this.from);
      element.dataset.liveInlineTo = String(this.to);
      element.dataset.liveInlineName = this.name;
    }
    const shadow = element.attachShadow({mode: "open"});
    const style = document.createElement("style");
    style.textContent = `:host { --preview-bg: #fff; --preview-surface: #fff; --preview-text: #20252b; --preview-muted: #687687; --preview-accent: #1769aa; --preview-border: #dce1e6; --preview-code-bg: #20252b; --preview-code-text: #f5f7fa; --preview-inline-code: #eef1f4; --preview-table-head: #f0f4f7; --preview-callout: #f4f8fb; display: block; max-width: 100%; white-space: normal; color: var(--preview-text); font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; line-height: 1.65; } :host([data-theme="dark"]), :host([data-theme="system"]) { --preview-bg: #12191e; --preview-surface: #1e282e; --preview-text: #e8eef1; --preview-muted: #aab8c0; --preview-accent: #75aaff; --preview-border: #35454d; --preview-code-bg: #12191e; --preview-code-text: #e8eef1; --preview-inline-code: #2b373d; --preview-table-head: #253137; --preview-callout: #223854; } @media (prefers-color-scheme: light) { :host([data-theme="system"]) { --preview-bg: #fff; --preview-surface: #fff; --preview-text: #20252b; --preview-muted: #687687; --preview-accent: #1769aa; --preview-border: #dce1e6; --preview-code-bg: #20252b; --preview-code-text: #f5f7fa; --preview-inline-code: #eef1f4; --preview-table-head: #f0f4f7; --preview-callout: #f4f8fb; } } :host(.live-rendered-inline) { display: inline; vertical-align: baseline; } .content-preview-content { min-width: 0; } ${this.inlineHost ? ".content-preview-content, p { display: inline; margin: 0; }" : ""} h1, h2, h3, h4, h5, h6 { margin: .5rem 0; } p { margin: .7rem 0; } pre { overflow-x: auto; padding: .75rem; background: var(--preview-code-bg); color: var(--preview-code-text); } pre.mermaid { background: var(--preview-surface); color: var(--preview-text); border: 1px dashed var(--preview-border); } code, .live-inline-code { padding: .08rem .25rem; border-radius: .2rem; background: var(--preview-inline-code); } pre code { padding: 0; background: transparent; } blockquote { margin: .75rem 0; padding-left: .8rem; border-left: 3px solid var(--preview-border); color: var(--preview-muted); } .callout { margin: .75rem 0; padding: .65rem .8rem; border-left: 3px solid var(--preview-accent); border-radius: .25rem; background: var(--preview-callout); } .callout-title { margin-bottom: .35rem; color: var(--preview-accent); font-weight: 700; } .footnotes { margin-top: 1.5rem; padding-top: .5rem; border-top: 1px solid var(--preview-border); font-size: .85em; } table { width: 100%; border-collapse: collapse; } th, td { padding: .35rem .5rem; border: 1px solid var(--preview-border); text-align: left; } th { background: var(--preview-table-head); } a, .live-link { color: var(--preview-accent); text-decoration: underline; } .live-image { padding: .15rem .4rem; border: 1px solid var(--preview-border); border-radius: .25rem; background: var(--preview-surface); } .live-math { font-family: Georgia, serif; font-style: italic; } ul:has(> li > .oxpio-task-input), ul:has(> li > p > .oxpio-task-input) { list-style: none; padding-left: 0; } ul:has(> li > .oxpio-task-input) > li, ul:has(> li > p > .oxpio-task-input) > li { padding-left: 0; } .oxpio-task-input { appearance: none; -webkit-appearance: none; display: inline-block; width: 1.25em; height: 1.25em; margin: 0 .35em 0 0; padding: 0; vertical-align: -.18em; border: 1px solid #aeb4bb; border-radius: .28em; background: transparent; background-position: 50% 50%; background-repeat: no-repeat; background-size: 100%; color: #687385; opacity: 1; } .oxpio-task-input:checked { border: none; border-radius: 0; background-image: none; background-color: currentColor; -webkit-mask-repeat: no-repeat; mask-repeat: no-repeat; -webkit-mask-size: 100%; mask-size: 100%; -webkit-mask-position: 50% 50%; mask-position: 50% 50%; } .oxpio-task-input[data-task='x']:checked, .oxpio-task-input[data-task='X']:checked { border-radius: .28em; background-color: #8b5cf6; background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Cpath d='m5 12 4 4L19 6' fill='none' stroke='white' stroke-linecap='round' stroke-linejoin='round' stroke-width='2.5'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='/']:checked { background-image: none; background-color: transparent; border: none; position: relative; overflow: hidden; } .oxpio-task-input[data-task='/']:checked::after { content: ''; display: block; position: absolute; inset: 0 auto 0 0; width: calc(50% - .5px); background-color: #8b5cf6; } .oxpio-task-input[data-task='?']:checked { border: 0; background-color: #eab308; background-size: 200% 90%; background-image: url('data:image/svg+xml,%3Csvg xmlns="http%3A%2F%2Fwww.w3.org%2F2000%2Fsvg" viewBox="0 0 16 16"%3E%3Cpath fill="white" fill-rule="evenodd" d="M4.475 5.458c-.284 0-.514-.237-.47-.517C4.28 3.24 5.576 2 7.825 2c2.25 0 3.767 1.36 3.767 3.215c0 1.344-.665 2.288-1.79 2.973c-1.1.659-1.414 1.118-1.414 2.01v.03a.5.5 0 0 1-.5.5h-.77a.5.5 0 0 1-.5-.495l-.003-.2c-.043-1.221.477-2.001 1.645-2.712c1.03-.632 1.397-1.135 1.397-2.028c0-.979-.758-1.698-1.926-1.698c-1.009 0-1.71.529-1.938 1.402c-.066.254-.278.461-.54.461h-.777ZM7.496 14c.622 0 1.095-.474 1.095-1.09c0-.618-.473-1.092-1.095-1.092c-.606 0-1.087.474-1.087 1.091S6.89 14 7.496 14Z"/%3E%3C/svg%3E'); } .oxpio-task-input[data-task='"']:checked { border: 0; background-color: #14b8a6; background-size: 75%; background-image: url('data:image/svg+xml,%3Csvg xmlns="http%3A%2F%2Fwww.w3.org%2F2000%2Fsvg" viewBox="0 0 24 24"%3E%3Cpath fill="white" d="M6.5 10c-.223 0-.437.034-.65.065c.069-.232.14-.468.254-.68c.114-.308.292-.575.469-.844c.148-.291.409-.488.601-.737c.201-.242.475-.403.692-.604c.213-.21.492-.315.714-.463c.232-.133.434-.28.65-.35l.539-.222l.474-.197l-.485-1.938l-.597.144c-.191.048-.424.104-.689.171c-.271.05-.56.187-.882.312c-.318.142-.686.238-1.028.466c-.344.218-.741.4-1.091.692c-.339.301-.748.562-1.05.945c-.33.358-.656.734-.909 1.162c-.293.408-.492.856-.702 1.299c-.19.443-.343.896-.468 1.336c-.237.882-.343 1.72-.384 2.437c-.034.718-.014 1.315.028 1.747c.015.204.043.402.063.539l.025.168l.026-.006A4.5 4.5 0 1 0 6.5 10z"/%3E%3C/svg%3E'); } .oxpio-task-input[data-task='>']:checked { color: #687385; transform: rotate(90deg); -webkit-mask-position: 50% 100%; mask-position: 50% 100%; -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath d='M10.894 2.553a1 1 0 0 0-1.788 0l-7 14a1 1 0 0 0 1.169 1.409l5-1.429A1 1 0 0 0 9 15.571V11a1 1 0 1 1 2 0v4.571a1 1 0 0 0 .725.962l5 1.428a1 1 0 0 0 1.17-1.408l-7-14z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='<']:checked { color: #687385; -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath fill-rule='evenodd' d='M10 18a8 8 0 1 0 0-16 8 8 0 0 0 0 16zm1-12a1 1 0 1 0-2 0v4a1 1 0 0 0 .293.707l2.828 2.829a1 1 0 1 0 1.415-1.415L11 9.586V6z' clip-rule='evenodd'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='-']:checked { color: #687385; -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath fill-rule='evenodd' d='M3 10a1 1 0 0 1 1-1h12a1 1 0 1 1 0 2H4a1 1 0 0 1-1-1z' clip-rule='evenodd'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='!']:checked { color: #f97316; -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath fill-rule='evenodd' d='M8.257 3.099c.765-1.36 2.722-1.36 3.486 0l5.58 9.92c.75 1.334-.213 2.98-1.742 2.98H4.42c-1.53 0-2.493-1.646-1.743-2.98l5.58-9.92zM11 13a1 1 0 1 1-2 0 1 1 0 0 1 2 0zm-1-8a1 1 0 0 0-1 1v3a1 1 0 0 0 2 0V6a1 1 0 0 0-1-1z' clip-rule='evenodd'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='*']:checked { color: #eab308; -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath d='M9.049 2.927c.3-.921 1.603-.921 1.902 0l1.07 3.292a1 1 0 0 0 .95.69h3.462c.969 0 1.371 1.24.588 1.81l-2.8 2.034a1 1 0 0 0-.364 1.118l1.07 3.292c.3.921-.755 1.688-1.54 1.118l-2.8-2.034a1 1 0 0 0-1.175 0l-2.8 2.034c-.784.57-1.838-.197-1.539-1.118l1.07-3.292a1 1 0 0 0-.364-1.118L2.98 8.72c-.783-.57-.38-1.81.588-1.81h3.461a.951.951 0 0 0 .951-.69l1.07-3.292z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='l']:checked { color: #ef4444; -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath fill-rule='evenodd' d='M5.05 4.05a7 7 0 1 1 9.9 9.9L10 18.9l-4.95-4.95a7 7 0 0 1 0-9.9ZM10 11a2 2 0 1 0 0-4 2 2 0 0 0 0 4Z' clip-rule='evenodd'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='b']:checked { color: #f97316; -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath d='M5 4a2 2 0 0 1 2-2h6a2 2 0 0 1 2 2v14l-5-2.5L5 18V4z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='i']:checked { background-color: #2563eb; background-image: url('data:image/svg+xml,%3Csvg xmlns="http%3A%2F%2Fwww.w3.org%2F2000%2Fsvg" viewBox="0 0 512 512"%3E%3Cpath fill="none" stroke="white" stroke-width="40" d="M196 220h64v172M187 396h138"/%3E%3Cpath fill="white" d="M256 160a32 32 0 1 1 32-32a32 32 0 0 1-32 32Z"/%3E%3C/svg%3E'); } .oxpio-task-input[data-task='S']:checked { background-color: #16a34a; background-image: url('data:image/svg+xml,%3Csvg xmlns="http%3A%2F%2Fwww.w3.org%2F2000%2Fsvg" viewBox="0 0 48 48"%3E%3Cpath fill="white" d="M26 8a2 2 0 1 0-4 0v2a8 8 0 1 0 0 16v8a4 4 0 0 1-3.773-2.666a2 2 0 0 0-3.771 1.332A8 8 0 0 0 22 38v2a2 2 0 1 0 4 0v-2a8 8 0 1 0 0-16v-8a4 4 0 0 1 3.773 2.666a2 2 0 0 0 3.771-1.332A8 8 0 0 0 26 10V8Z"/%3E%3C/svg%3E'); } .oxpio-task-input[data-task='I']:checked { color: #eab308; -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath d='M11 3a1 1 0 1 0-2 0v1a1 1 0 1 0 2 0V3zM15.657 5.757a1 1 0 0 0-1.414-1.414l-.707.707a1 1 0 0 0 1.414 1.414l.707-.707zM18 10a1 1 0 0 1-1 1h-1a1 1 0 1 1 0-2h1a1 1 0 0 1 1 1zM8 16v-1h4v1a2 2 0 1 1-4 0zM12 14a4 4 0 1 0-4.954 0c.27.213.462.519.476.859h4.002z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='f']:checked { color: #ef4444; -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath d='M12.395 2.553a1 1 0 0 0-1.45-.385C7.8 4.3 7.1 8.2 7 10.7A2.6 2.6 0 0 1 5.5 8.2A7 7 0 1 0 15 13c0-3.2-2.2-5.7-2.6-10.4Z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='k']:checked { color: #eab308; -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath fill-rule='evenodd' d='M18 8a6 6 0 0 1-7.743 5.743L10 14l-1 1-1 1H6v2H2v-4l4.257-4.257A6 6 0 1 1 18 8zm-6-4a1 1 0 1 0 0 2 2 2 0 0 1 2 2 1 1 0 1 0 2 0 4 4 0 0 0-4-4z' clip-rule='evenodd'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='w']:checked { color: #7c3aed; -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath d='M2 13v4a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-4a4 4 0 0 1-2 1 3 3 0 0 1-3-2 3 3 0 0 1-6 0 3 3 0 0 1-3 2 4 4 0 0 1-2-1Z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='p']:checked { color: #16a34a; -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath d='M2 10.5a1.5 1.5 0 1 1 3 0v6a1.5 1.5 0 1 1-3 0v-6zM6 10.333v5.43a2 2 0 0 0 1.106 1.79L8 18h6.36a2 2 0 0 0 1.96-1.6l1.2-6A2 2 0 0 0 15.56 8H12V4a2 2 0 0 0-2-2 1 1 0 0 0-1 1v.667a4 4 0 0 1-.8 2.4L6.8 7.933a4 4 0 0 0-.8 2.4z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='c']:checked { color: #f97316; -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath d='M18 9.5a1.5 1.5 0 1 1-3 0v-6a1.5 1.5 0 1 1 3 0v6zM14 9.667v-5.43A2 2 0 0 0 11.9 2H5.64a2 2 0 0 0-1.96 1.6l-1.2 6A2 2 0 0 0 4.44 12H8v4a2 2 0 0 0 2 2 1 1 0 0 0 1-1v-.667a4 4 0 0 0 .8-2.4l1.4-1.866a4 4 0 0 0 .8-2.4z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='u']:checked { color: #16a34a; -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath d='M12 7a1 1 0 1 0 0-2h5a1 1 0 0 1 1 1v5a1 1 0 1 1-2 0V8.414l-4.293 4.293a1 1 0 0 1-1.414 0L8 10.414l-4.293 4.293a1 1 0 0 1-1.414-1.414l5-5a1 1 0 0 1 1.414 0L11 10.586 14.586 7H12z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='d']:checked { color: #ef4444; -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath d='M12 13a1 1 0 1 0 0 2h5a1 1 0 0 0 1-1V9a1 1 0 1 0-2 0v2.586L12.707 7.293a1 1 0 0 0-1.414 0L8 10.586 3.707 6.293a1 1 0 0 0-1.414 1.414l5 5a1 1 0 0 0 1.414 0L11 10.414 14.586 13H12z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='P']:checked, .oxpio-task-input[data-task='M']:checked, .oxpio-task-input[data-task='D']:checked { -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Cpath d='M1.5 3.25a2.25 2.25 0 1 1 3 2.122v5.256a2.25 2.25 0 1 1-1.5 0V5.372A2.25 2.25 0 0 1 1.5 3.25Zm5.677-.177L9.573.677A.25.25 0 0 1 10 .854V2.5h1A2.5 2.5 0 0 1 13.5 5v5.628a2.25 2.25 0 1 1-1.5 0V5a1 1 0 0 0-1-1h-1v1.646a.25.25 0 0 1-.427.177L7.177 3.427a.25.25 0 0 1 0-.354Z'%3E%3C/path%3E%3C/svg%3E"); } .oxpio-task-input[data-task='P']:checked { color: #16a34a; } .oxpio-task-input[data-task='M']:checked { color: #7c3aed; } .oxpio-task-input[data-task='D']:checked { color: #687385; } .oxpio-task-input[data-task='\"']:checked { border: 0; background-color: #14b8a6; background-size: 75%; background-image: url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Cpath fill='white' d='M5 5h6v7H8v4h3v3H5v-7h6V5H5zm8 0h6v7h-3v4h3v3h-6v-7h6V5h-6z'/%3E%3C/svg%3E\"); } .oxpio-task-input[data-task='P']:checked { color: #16a34a; -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Cpath d='M1.5 3.25a2.25 2.25 0 1 1 3 2.122v5.256a2.251 2.251 0 1 1-1.5 0V5.372A2.25 2.25 0 0 1 1.5 3.25Zm5.677-.177L9.573.677A.25.25 0 0 1 10 .854V2.5h1A2.5 2.5 0 0 1 13.5 5v5.628a2.251 2.251 0 1 1-1.5 0V5a1 1 0 0 0-1-1h-1v1.646a.25.25 0 0 1-.427.177L7.177 3.427a.25.25 0 0 1 0-.354ZM3.75 2.5a.75.75 0 1 0 0 1.5.75.75 0 0 0 0-1.5Zm0 9.5a.75.75 0 1 0 0 1.5.75.75 0 0 0 0-1.5Zm8.25.75a.75.75 0 1 0 1.5 0 .75.75 0 0 0-1.5 0Z'%3E%3C/path%3E%3C/svg%3E"); } .oxpio-task-input[data-task='M']:checked { color: #7c3aed; -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Cpath d='M5.45 5.154A4.25 4.25 0 0 0 9.25 7.5h1.378a2.251 2.251 0 1 1 0 1.5H9.25A5.734 5.734 0 0 1 5 7.123v3.505a2.25 2.25 0 1 1-1.5 0V5.372a2.25 2.25 0 1 1 1.95-.218ZM4.25 13.5a.75.75 0 1 0 0-1.5.75.75 0 0 0 0 1.5Zm8.5-4.5a.75.75 0 1 0 0-1.5.75.75 0 0 0 0 1.5ZM5 3.25a.75.75 0 1 0 0 .005V3.25Z'%3E%3C/path%3E%3C/svg%3E"); } .oxpio-task-input[data-task='D']:checked { color: #687385; -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Cpath d='M3.25 1A2.25 2.25 0 0 1 4 5.372v5.256a2.251 2.251 0 1 1-1.5 0V5.372A2.251 2.251 0 0 1 3.25 1Zm9.5 14a2.25 2.25 0 1 1 0-4.5 2.25 2.25 0 0 1 0 4.5ZM2.5 3.25a.75.75 0 1 0 1.5 0 .75.75 0 0 0-1.5 0ZM3.25 12a.75.75 0 1 1 0 1.5.75.75 0 0 0 0-1.5Zm9.5 0a.75.75 0 1 0 0-1.5.75.75 0 0 0-1.5 0ZM14 7.5a1.25 1.25 0 1 1-2.5 0 1.25 1.25 0 0 1 2.5 0Zm0-4.25a1.25 1.25 0 1 1-2.5 0 1.25 1.25 0 0 1-2.5 0Z'%3E%3C/path%3E%3C/svg%3E"); } .oxpio-task-input[data-task='c']:checked { mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath d='M18 9.5a1.5 1.5 0 1 1-3 0v-6a1.5 1.5 0 1 1 3 0v6zM14 9.667v-5.43A2 2 0 0 0 11.9 2H5.64a2 2 0 0 0-1.96 1.6l-1.2 6A2 2 0 0 0 4.44 12H8v4a2 2 0 0 0 2 2 1 1 0 0 0 1-1v-.667a4 4 0 0 0 .8-2.4l1.4-1.866a4 4 0 0 0 .8-2.4z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='P']:checked { mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Cpath d='M1.5 3.25a2.25 2.25 0 1 1 3 2.122v5.256a2.251 2.251 0 1 1-1.5 0V5.372A2.25 2.25 0 0 1 1.5 3.25Zm5.677-.177L9.573.677A.25.25 0 0 1 10 .854V2.5h1A2.5 2.5 0 0 1 13.5 5v5.628a2.251 2.251 0 1 1-1.5 0V5a1 1 0 0 0-1-1h-1v1.646a.25.25 0 0 1-.427.177L7.177 3.427a.25.25 0 0 1 0-.354Z'%3E%3C/path%3E%3C/svg%3E"); } .oxpio-task-input[data-task='M']:checked { mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Cpath d='M5.45 5.154A4.25 4.25 0 0 0 9.25 7.5h1.378a2.251 2.251 0 1 1 0 1.5H9.25A5.734 5.734 0 0 1 5 7.123v3.505a2.25 2.25 0 1 1-1.5 0V5.372a2.25 2.25 0 1 1 1.95-.218ZM4.25 13.5a.75.75 0 1 0 0-1.5.75.75 0 0 0 0 1.5Zm8.5-4.5a.75.75 0 1 0 0-1.5.75.75 0 0 0 0 1.5ZM5 3.25a.75.75 0 1 0 0 .005V3.25Z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='D']:checked { mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Cpath d='M3.25 1A2.25 2.25 0 0 1 4 5.372v5.256a2.251 2.251 0 1 1-1.5 0V5.372A2.251 2.251 0 0 1 3.25 1Zm9.5 14a2.25 2.25 0 1 1 0-4.5 2.25 2.25 0 0 1 0 4.5ZM2.5 3.25a.75.75 0 1 0 1.5 0 .75.75 0 0 0-1.5 0ZM3.25 12a.75.75 0 1 0 0 1.5.75.75 0 0 0 0-1.5Zm9.5 0a.75.75 0 1 0 0-1.5.75.75 0 0 0-1.5 0ZM14 7.5a1.25 1.25 0 1 1-2.5 0 1.25 1.25 0 0 1 2.5 0Zm0-4.25a1.25 1.25 0 1 1-2.5 0 1.25 1.25 0 0 1-2.5 0Z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='>']:checked { mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath d='M10.894 2.553a1 1 0 0 0-1.788 0l-7 14a1 1 0 0 0 1.169 1.409l5-1.429A1 1 0 0 0 9 15.571V11a1 1 0 1 1 2 0v4.571a1 1 0 0 0 .725.962l5 1.428a1 1 0 0 0 1.17-1.408l-7-14z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='<']:checked { mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath fill-rule='evenodd' d='M10 18a8 8 0 1 0 0-16 8 8 0 0 0 0 16zm1-12a1 1 0 1 0-2 0v4a1 1 0 0 0 .293.707l2.828 2.829a1 1 0 1 0 1.415-1.415L11 9.586V6z' clip-rule='evenodd'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='-']:checked { mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath fill-rule='evenodd' d='M3 10a1 1 0 0 1 1-1h12a1 1 0 1 1 0 2H4a1 1 0 0 1-1-1z' clip-rule='evenodd'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='!']:checked { mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath fill-rule='evenodd' d='M8.257 3.099c.765-1.36 2.722-1.36 3.486 0l5.58 9.92c.75 1.334-.213 2.98-1.742 2.98H4.42c-1.53 0-2.493-1.646-1.743-2.98l5.58-9.92zM11 13a1 1 0 1 1-2 0 1 1 0 0 1 2 0zm-1-8a1 1 0 0 0-1 1v3a1 1 0 0 0 2 0V6a1 1 0 0 0-1-1z' clip-rule='evenodd'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='*']:checked { mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath d='M9.049 2.927c.3-.921 1.603-.921 1.902 0l1.07 3.292a1 1 0 0 0 .95.69h3.462c.969 0 1.371 1.24.588 1.81l-2.8 2.034a1 1 0 0 0-.364 1.118l1.07 3.292c.3.921-.755 1.688-1.54 1.118l-2.8-2.034a1 1 0 0 0-1.175 0l-2.8 2.034c-.784.57-1.838-.197-1.539-1.118l1.07-3.292a1 1 0 0 0-.364-1.118L2.98 8.72c-.783-.57-1.371-1.81.588-1.81h3.461a.951.951 0 0 0 .951-.69l1.07-3.292z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='l']:checked { mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath fill-rule='evenodd' d='M5.05 4.05a7 7 0 1 1 9.9 9.9L10 18.9l-4.95-4.95a7 7 0 0 1 0-9.9ZM10 11a2 2 0 1 0 0-4 2 2 0 0 0 0 4Z' clip-rule='evenodd'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='b']:checked { mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath d='M5 4a2 2 0 0 1 2-2h6a2 2 0 0 1 2 2v14l-5-2.5L5 18V4z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='I']:checked { mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath d='M11 3a1 1 0 1 0-2 0v1a1 1 0 1 0 2 0V3zM15.657 5.757a1 1 0 0 0-1.414-1.414l-.707.707a1 1 0 0 0 1.414 1.414l.707-.707zM18 10a1 1 0 0 1-1 1h-1a1 1 0 1 1 0-2h1a1 1 0 0 1 1 1zM8 16v-1h4v1a2 2 0 1 1-4 0zM12 14a4 4 0 1 0-4.954 0c.27.213.462.519.476.859h4.002z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='f']:checked { mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath d='M12.395 2.553a1 1 0 0 0-1.45-.385C7.8 4.3 7.1 8.2 7 10.7A2.6 2.6 0 0 1 5.5 8.2A7 7 0 1 0 15 13c0-3.2-2.2-5.7-2.6-10.4Z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='k']:checked { mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath fill-rule='evenodd' d='M18 8a6 6 0 0 1-7.743 5.743L10 14l-1 1-1 1H6v2H2v-4l4.257-4.257A6 6 0 1 1 18 8zm-6-4a1 1 0 1 0 0 2 2 2 0 0 1 2 2 1 1 0 1 0 2 0 4 4 0 0 0-4-4z' clip-rule='evenodd'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='w']:checked { mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath d='M2 13v4a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-4a4 4 0 0 1-2 1 3 3 0 0 1-3-2 3 3 0 0 1-6 0 3 3 0 0 1-3 2 4 4 0 0 1-2-1Z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='p']:checked { mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath d='M2 10.5a1.5 1.5 0 1 1 3 0v6a1.5 1.5 0 1 1-3 0v-6zM6 10.333v5.43a2 2 0 0 0 1.106 1.79L8 18h6.36a2 2 0 0 0 1.96-1.6l1.2-6A2 2 0 0 0 15.56 8H12V4a2 2 0 0 0-2-2 1 1 0 0 0-1 1v.667a4 4 0 0 1-.8 2.4L6.8 7.933a4 4 0 0 0-.8 2.4z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='u']:checked { mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath d='M12 7a1 1 0 1 0 0-2h5a1 1 0 0 1 1 1v5a1 1 0 1 1-2 0V8.414l-4.293 4.293a1 1 0 0 1-1.414 0L8 10.414l-4.293 4.293a1 1 0 0 1-1.414-1.414l5-5a1 1 0 0 1 1.414 0L11 10.586 14.586 7H12z'/%3E%3C/svg%3E"); } .oxpio-task-input[data-task='d']:checked { mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 20 20'%3E%3Cpath d='M12 13a1 1 0 1 0 0 2h5a1 1 0 0 0 1-1V9a1 1 0 1 0-2 0v2.586L12.707 7.293a1 1 0 0 0-1.414 0L8 10.586 3.707 6.293a1 1 0 0 0-1.414 1.414l5 5a1 1 0 0 0 1.414 0L11 10.414 14.586 13H12z'/%3E%3C/svg%3E"); } :host([data-theme="dark"]) .oxpio-task-input[data-task='?']:checked, :host-context(html[data-theme="dark"]) .oxpio-task-input[data-task='?']:checked { background-image: url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Cpath fill='black' fill-opacity='.8' d='M4.475 5.458c-.284 0-.514-.237-.47-.517C4.28 3.24 5.576 2 7.825 2c2.25 0 3.767 1.36 3.767 3.215c0 1.344-.665 2.288-1.79 2.973c-1.1.659-1.414 1.118-1.414 2.01v.03a.5.5 0 0 1-.5.5h-.77a.5.5 0 0 1-.5-.495l-.003-.2c-.043-1.221.477-2.001 1.645-2.712c1.03-.632 1.397-1.135 1.397-2.028c0-.979-.758-1.698-1.926-1.698c-1.009 0-1.71.529-1.938 1.402c-.066.254-.278.461-.54.461h-.777ZM7.496 14c.622 0 1.095-.474 1.095-1.09c0-.618-.473-1.092-1.095-1.092c-.606 0-1.087.474-1.087 1.091S6.89 14 7.496 14Z\"/%3E%3C/svg%3E\"); } :host([data-theme="dark"]) .oxpio-task-input[data-task='\"']:checked, :host-context(html[data-theme="dark"]) .oxpio-task-input[data-task='\"']:checked { background-image: url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Cpath fill='black' fill-opacity='.7' d='M5 5h6v7H8v4h3v3H5v-7h6V5H5zm8 0h6v7h-3v4h3v3h-6v-7h6V5h-6z'/%3E%3C/svg%3E\"); } @media (prefers-color-scheme: dark) { :host([data-theme="system"]) .oxpio-task-input[data-task='?']:checked { background-image: url('data:image/svg+xml,%3Csvg xmlns="http%3A%2F%2Fwww.w3.org%2F2000%2Fsvg" viewBox="0 0 16 16"%3E%3Cpath fill="black" fill-opacity="0.8" d="M4.475 5.458c-.284 0-.514-.237-.47-.517C4.28 3.24 5.576 2 7.825 2c2.25 0 3.767 1.36 3.767 3.215c0 1.344-.665 2.288-1.79 2.973c-1.1.659-1.414 1.118-1.414 2.01v.03a.5.5 0 0 1-.5.5h-.77a.5.5 0 0 1-.5-.495l-.003-.2c-.043-1.221.477-2.001 1.645-2.712c1.03-.632 1.397-1.135 1.397-2.028c0-.979-.758-1.698-1.926-1.698c-1.009 0-1.71.529-1.938 1.402c-.066.254-.278.461-.54.461h-.777ZM7.496 14c.622 0 1.095-.474 1.095-1.09c0-.618-.473-1.092-1.095-1.092c-.606 0-1.087.474-1.087 1.091S6.89 14 7.496 14Z"/%3E%3C/svg%3E'); } :host([data-theme="system"]) .oxpio-task-input[data-task='\"']:checked { background-image: url('data:image/svg+xml,%3Csvg xmlns="http%3A%2F%2Fwww.w3.org%2F2000%2Fsvg" viewBox="0 0 24 24"%3E%3Cpath fill="black" fill-opacity="0.7" d="M5 5h6v7H8v4h3v3H5v-7h6V5H5zm8 0h6v7h-3v4h3v3h-6v-7h6V5h-6z"/%3E%3C/svg%3E'); } } li:has(> input[data-task='x']:checked), li:has(> input[data-task='X']:checked), li:has(> input[data-task='-']:checked), li:has(> p > input[data-task='x']:checked), li:has(> p > input[data-task='X']:checked), li:has(> p > input[data-task='-']:checked) { text-decoration: line-through solid currentColor 1px; }${this.styles}`;
    const mathStyle = document.createElement("link");
    mathStyle.rel = "stylesheet";
    mathStyle.href = new URL("editor-runtime/katex.min.css", document.baseURI).href;
    const taskStatusStyle = document.createElement("link");
    taskStatusStyle.rel = "stylesheet";
    taskStatusStyle.href = new URL("editor-runtime/task-status.css", document.baseURI).href;
    const content = document.createElement("div");
    content.className = "content-preview-content";
    content.innerHTML = this.html;
    const inlineElements = [...content.querySelectorAll("strong, em, del, a, code, img")];
    const inlineSelectors = renderedInlineSelectors;
    const usedInlineElements = new Set();
    (this.inline || []).forEach(range => {
      const selector = inlineSelectors[range.name];
      const element = inlineElements.find(candidate => candidate.matches(selector) && !usedInlineElements.has(candidate));
      if (!element) return;
      usedInlineElements.add(element);
      element.setAttribute("data-live-inline-from", String(range.from));
      element.setAttribute("data-live-inline-to", String(range.to));
      element.setAttribute("data-live-inline-name", range.name);
    });
    content.querySelectorAll("[data-live-inline-offset-from]").forEach(inline => {
      const from = this.from + Number(inline.getAttribute("data-live-inline-offset-from"));
      const to = this.from + Number(inline.getAttribute("data-live-inline-offset-to"));
      inline.setAttribute("data-live-inline-from", String(from));
      inline.setAttribute("data-live-inline-to", String(to));
      inline.removeAttribute("data-live-inline-offset-from");
      inline.removeAttribute("data-live-inline-offset-to");
    });
    content.querySelectorAll("[data-live-math-from]").forEach(math => {
      const from = utf8BoundaryMap(this.source).get(Number(math.getAttribute("data-live-math-from"))) ?? this.from;
      const to = utf8BoundaryMap(this.source).get(Number(math.getAttribute("data-live-math-to"))) ?? this.to;
      math.setAttribute("data-live-inline-from", String(from));
      math.setAttribute("data-live-inline-to", String(to));
      math.setAttribute("data-live-inline-name", "Math");
      math.removeAttribute("data-live-math-from");
      math.removeAttribute("data-live-math-to");
    });
    const renderMath = () => content.querySelectorAll(".oxpio-math").forEach(math => {
      if (!window.katex?.renderToString || math.querySelector(".katex, .katex-error")) return;
      const raw = math.textContent.trim();
      const display = raw.startsWith("$$") || raw.startsWith("\\[");
      const wrapped = raw.startsWith("\\(") || raw.startsWith("\\[");
      const formula = display || wrapped ? raw.slice(2, -2).trim() : raw.slice(1, -1);
      try { math.innerHTML = window.katex.renderToString(formula, {displayMode: display, throwOnError: false, trust: false}); } catch { /* KaTeX keeps the source visible. */ }
    });
    if (window.katex) renderMath(); else ensureMathRuntime().then(renderMath).catch(() => {});
    const mermaidBlocks = [...content.querySelectorAll("pre code.language-mermaid")].map(code => {
      const block = code.parentElement;
      const source = code.textContent;
      block.classList.add("mermaid");
      block.textContent = source;
      return {block, source};
    });
    if (mermaidBlocks.length) {
      ensureMermaidRuntime().then(async mermaid => {
        mermaid.initialize({startOnLoad: false, securityLevel: "strict", theme: "neutral"});
        for (const {block, source} of mermaidBlocks) {
          const rendered = await mermaid.render(`oxpio-mermaid-${++mermaidRenderID}`, source);
          block.innerHTML = rendered.svg;
          rendered.bindFunctions?.(block);
        }
      }).catch(() => {});
    }
    shadow.append(style, taskStatusStyle, mathStyle, content);
    return element;
  }
}
function liveReplace(from, to) { return from < to ? Decoration.replace({}).range(from, to) : null; }
function liveWidget(from, to, text, className) { return from < to ? Decoration.replace({widget: new LivePreviewWidget(text, className)}).range(from, to) : null; }
function findSyntaxNodes(viewState, from, to, names) {
  const nodes = [];
  syntaxTree(viewState).iterate({enter(node) {
    if (names.has(node.name) && node.from >= from && node.to <= to) nodes.push({from: node.from, to: node.to, name: node.name});
  }});
  return nodes;
}
function liveNodeMarkerRanges(viewState, node, activeInline) {
  if (activeInline && activeInline.from === node.from && activeInline.to === node.to) return [];
  const children = findSyntaxNodes(viewState, node.from, node.to, new Set(["EmphasisMark", "CodeMark", "LinkMark", "URL", "StrikethroughMark"]));
  const ranges = [];
  const addReplace = child => {
    const range = liveReplace(child.from, child.to);
    if (range) ranges.push(range);
  };
  if (node.name === "Image") {
    const source = viewState.sliceDoc(node.from, node.to);
    const label = source.match(/^!\[([^\]]*)\]/u)?.[1] || "image";
    const range = liveWidget(node.from, node.to, `🖼 ${label}`, "live-image");
    return range ? [range] : [];
  }
  if (node.name === "Link") {
    const marks = children.filter(child => child.name === "LinkMark").sort((a, b) => a.from - b.from);
    children.filter(child => child.name === "LinkMark" || child.name === "URL").forEach(addReplace);
    if (marks.length >= 2 && marks[0].to <= marks[1].from) {
      ranges.push(Decoration.mark({class: "live-link"}).range(marks[0].to, marks[1].from));
    }
    return ranges;
  }
  if (node.name === "InlineCode") {
    children.filter(child => child.name === "CodeMark").forEach(addReplace);
    const marks = children.filter(child => child.name === "CodeMark").sort((a, b) => a.from - b.from);
    if (marks.length >= 2) ranges.push(Decoration.mark({class: "live-inline-code"}).range(marks[0].to, marks[1].from));
    return ranges;
  }
  const marks = children.filter(child => child.name.endsWith("Mark")).sort((a, b) => a.from - b.from);
  marks.forEach(addReplace);
  if (marks.length >= 2) {
    const className = node.name === "StrongEmphasis" ? "live-strong" : node.name === "Strikethrough" ? "live-strikethrough" : "live-emphasis";
    ranges.push(Decoration.mark({class: className}).range(marks[0].to, marks[marks.length - 1].from));
  }
  return ranges;
}
function sharedMathContext(block, source, activeFrom = null, activeTo = null) {
  if (!block?.html || !source) return null;
  const root = document.createElement("div");
  root.innerHTML = block.html;
  const boundaries = utf8BoundaryMap(source);
  const mathNodes = [...root.querySelectorAll(".oxpio-math[data-live-math-from]")];
  const math = mathNodes.find(candidate => boundaries.get(Number(candidate.getAttribute("data-live-math-from"))) === activeFrom && boundaries.get(Number(candidate.getAttribute("data-live-math-to"))) === activeTo) || mathNodes[0];
  if (!math) return null;
  const from = boundaries.get(Number(math.getAttribute("data-live-math-from")));
  const to = boundaries.get(Number(math.getAttribute("data-live-math-to")));
  if (from === undefined || to === undefined) return null;
  const beforeRange = document.createRange();
  beforeRange.selectNodeContents(root);
  beforeRange.setEndBefore(math);
  const afterRange = document.createRange();
  afterRange.selectNodeContents(root);
  afterRange.setStartAfter(math);
  const before = document.createElement("div");
  before.append(beforeRange.cloneContents());
  const after = document.createElement("div");
  after.append(afterRange.cloneContents());
  return {from, to, before: before.innerHTML, after: after.innerHTML};
}
function renderedLivePreviewDecorations(viewState, active, rendered) {
  if (!rendered?.blocks?.length || rendered.body !== viewState.doc.toString()) return null;
  const blocks = rendered.blocks;
  const activeBlock = blocks.find(block => active && block.from <= active.from && block.to >= active.to);
  const ranges = [];
  const revealAll = viewState.selection.main.from === 0 && viewState.selection.main.to === viewState.doc.length;
  for (const block of blocks) {
    const isTable = block.name === "Table";
    if (active?.name === "Math" && activeBlock === block) {
      const context = sharedMathContext(block, rendered.source || rendered.body, active.from, active.to);
      if (context && context.from === active.from && context.to === active.to) {
        if (block.from < active.from && context.before) ranges.push(Decoration.replace({widget: new LiveRenderedWidget(context.before, rendered.styles, block.from, active.from, block.name, rendered.source || rendered.body, [], true)}).range(block.from, active.from));
        if (active.to < block.to && context.after) ranges.push(Decoration.replace({widget: new LiveRenderedWidget(context.after, rendered.styles, active.to, block.to, block.name, rendered.source || rendered.body, [], true)}).range(active.to, block.to));
        continue;
      }
    }
    if (revealAll) {
      ranges.push(...activeSourceDecorations(viewState, {from: block.from, to: block.to, name: block.name}));
    } else if (!activeBlock || block !== activeBlock || isTable) {
      ranges.push(Decoration.replace({widget: new LiveRenderedWidget(block.html, rendered.styles, block.from, block.to, block.name, rendered.source || rendered.body, block.inline), block: true}).range(block.from, block.to));
    } else {
      ranges.push(...activeSourceDecorations(viewState, active, block, rendered.styles));
    }
  }
  return ranges.length ? Decoration.set(ranges.sort((a, b) => a.from - b.from || a.to - b.to), true) : Decoration.none;
}
function buildLivePreviewDecorations(viewState, active, rendered) {
  if (state.surface !== "write" || state.mode !== "visual") return Decoration.none;
  return renderedLivePreviewDecorations(viewState, active, rendered) || Decoration.none;
}
const livePreviewDecorations = StateField.define({
  create: viewState => {
    const active = findActiveSyntaxNode(viewState, viewState.selection.main.head);
    const rendered = emptyRenderDocument(viewState);
    return {active, rendered, decorations: buildLivePreviewDecorations(viewState, active, rendered)};
  },
  update(value, transaction) {
    let active = value.active;
    let rendered = value.rendered;
    let explicitActive = false;
    let coreRendered = false;
    for (const effect of transaction.effects) {
      if (effect.is(livePreviewActive)) { active = effect.value; explicitActive = true; }
      if (effect.is(livePreviewCore)) { rendered = effect.value; coreRendered = true; }
    }
    if (transaction.docChanged) rendered = preserveRenderedDocument(rendered, transaction);
    if (transaction.selection && !explicitActive) active = findActiveSyntaxNode(transaction.state, transaction.state.selection.main.head);
    if (!transaction.docChanged && !transaction.selection && !explicitActive && !coreRendered) return value;
    return {active, rendered, decorations: buildLivePreviewDecorations(transaction.state, active, rendered)};
  },
  provide: field => EditorView.decorations.from(field, value => value.decorations),
});
function editorExtensions() {
  return [basicSetup, markdown({extensions: GFM}), livePreviewDecorations, indentation.of(indentExtensions()), EditorView.lineWrapping, editable.of(EditorView.editable.of(!state.busy)),
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
        const keyStart = performance.now();
        requestAnimationFrame(() => {
          livePreviewMetrics.keyToPaintMs.push(performance.now() - keyStart);
          if (livePreviewMetrics.keyToPaintMs.length > 200) livePreviewMetrics.keyToPaintMs.shift();
        });
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
  updateUploadGuidance();
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
function uploadTargetPath(file) {
  const folder = $("#upload-folder").value.trim();
  return folder ? `${folder}/${file.name}` : file.name;
}
function updateUploadGuidance(file = $("#media-upload").files?.[0]) {
  const warning = $("#upload-parent-warning");
  const message = file && /\.md$/iu.test(file.name) ? fileMarkdownWarning(uploadTargetPath(file), $("#upload-kind").value) : "";
  warning.textContent = message;
  warning.hidden = !message;
}
async function uploadMedia(file) {
  if (!file || state.busy) return;
  if (file instanceof FileList && file.length !== 1) { status("Upload exactly one file."); return; }
  const warning = file instanceof File ? fileMarkdownWarning(uploadTargetPath(file), $("#upload-kind").value) : "";
  if (warning) {
    updateUploadGuidance(file);
    diagnostics([], `Upload blocked: ${warning}`);
    status(`Upload blocked: ${warning}`);
    return;
  }
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
  finally { stopProgress(); busy(false); $("#media-upload").value = ""; updateUploadGuidance(); }
}
function fileSectionIndexPath(parent) {
  return parent === "." ? "_index.md" : `${parent}/_index.md`;
}
function fileParentSectionWarning(relPath) {
  if (!Array.isArray(state.fileEntries) || !/\.md$/iu.test(relPath)) return "";
  const isSectionIndex = /(^|\/)[_]index\.md$/iu.test(relPath);
  const slash = relPath.lastIndexOf("/");
  let parent = slash < 0 ? "." : relPath.slice(0, slash);
  if (isSectionIndex) {
    if (relPath === "_index.md") return "";
    const parentSlash = parent.lastIndexOf("/");
    parent = parentSlash < 0 ? "." : parent.slice(0, parentSlash);
  }
  const sectionPath = fileSectionIndexPath(parent);
  const section = state.fileEntries.find(entry => entry.kind === "file" && entry.path === sectionPath && entry.sourceKind === "section");
  if (section) return "";
  const folder = parent === "." ? "the vault root" : `folder \"${parent}\"`;
  return `${folder} is not a section yet. Create \"${sectionPath}\" as a Section index first.`;
}
function articleCreationWarning(relPath) {
  if (/(^|\/)[_]index\.md$/iu.test(relPath)) return "Articles cannot be named _index.md. Use New Markdown and choose Section index instead.";
  return fileParentSectionWarning(relPath);
}
function fileMarkdownWarning(relPath, kind) {
  const looksLikeSectionIndex = /(^|\/)[_]index\.md$/iu.test(relPath);
  const exactSectionIndex = /(^|\/)[_]index\.md$/.test(relPath);
  if (looksLikeSectionIndex && !exactSectionIndex) return "Section index files must use the exact filename _index.md.";
  if (looksLikeSectionIndex && kind !== "section-index") return "Files named _index.md must use Section index kind.";
  if (!looksLikeSectionIndex && kind === "section-index") return "Section index files must be named _index.md.";
  return fileParentSectionWarning(relPath);
}
function updateFileDialogGuidance() {
  const markdown = state.fileDialogMode === "markdown";
  const warning = $("#file-parent-warning");
  const message = markdown ? fileMarkdownWarning($("#file-path").value.trim(), $("#file-kind").value) : "";
  warning.textContent = message;
  warning.hidden = !message;
  $("#file-submit").disabled = Boolean(message);
}
function updateNewDialogGuidance() {
  const warning = $("#new-parent-warning");
  const message = articleCreationWarning($("#new-form input[name=path]").value.trim());
  warning.textContent = message;
  warning.hidden = !message;
  $("#new-submit").disabled = Boolean(message);
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
  $("#file-model-hint").hidden = !markdown;
  $("#file-parent-warning").hidden = true;
  $("#file-title").required = markdown;
  if (markdown) {
    $("#file-title").value = "";
    $("#file-type").value = "doc";
    $("#file-date").value = "";
    $("#file-kind").value = "article";
  }
  $("#file-submit").disabled = false;
  dialog.showModal();
  updateFileDialogGuidance();
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
  const target = event.target instanceof Element ? event.target.closest(".live-rendered-block, .live-rendered-inline") : null;
  if (!target) return;
  const path = event.composedPath?.() || [];
  const inline = path.find(node => node instanceof Element && node.hasAttribute("data-live-inline-from"));
  const block = path.find(node => node instanceof Element && node.hasAttribute("data-live-block-from"));
  let position;
  try {
    position = inline ? Number(inline.getAttribute("data-live-inline-from")) : block ? Number(block.getAttribute("data-live-block-from")) : editor.posAtDOM(target, 0);
  } catch { position = editor.state.selection.main.head; }
  const activeInline = inline ? {from: Number(inline.getAttribute("data-live-inline-from")), to: Number(inline.getAttribute("data-live-inline-to")), name: inline.getAttribute("data-live-inline-name") || ""} : block ? {from: Number(block.getAttribute("data-live-block-from")), to: Number(block.getAttribute("data-live-block-to")), name: block.getAttribute("data-live-block-name") || ""} : findActiveSyntaxNode(editor.state, position);
  event.preventDefault();
  editor.focus();
  editor.dispatch({selection: {anchor: position}, effects: livePreviewActive.of(activeInline)});
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
$("#file-path").oninput = updateFileDialogGuidance;
$("#file-kind").onchange = updateFileDialogGuidance;
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
  if (mode === "markdown" && fileMarkdownWarning(pathValue, $("#file-kind").value)) {
    updateFileDialogGuidance();
    $("#file-path").focus();
    return;
  }
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
$("#new").onclick = () => { if (confirmDiscard()) { $("#new-dialog").showModal(); updateNewDialogGuidance(); } };
$("#new-form input[name=path]").oninput = updateNewDialogGuidance;
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
  if (articleCreationWarning(form.get("path")?.trim() || "")) {
    updateNewDialogGuidance();
    $("#new-form input[name=path]").focus();
    return;
  }
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
$("#upload-folder").oninput = () => updateUploadGuidance();
$("#upload-kind").onchange = () => updateUploadGuidance();
$("#media-upload").onchange = event => { if (event.target.files.length !== 1) { saveState("failed", "Upload failed"); diagnostics([], "Upload exactly one file; no files were written."); status("Upload exactly one file; no files were written."); event.target.value = ""; return; } updateUploadGuidance(event.target.files[0]); uploadMedia(event.target.files[0]); };
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
