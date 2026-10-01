import {basicSetup} from "codemirror";
import {markdown} from "@codemirror/lang-markdown";
import {indentWithTab} from "@codemirror/commands";
import {Compartment, EditorState, Prec} from "@codemirror/state";
import {EditorView, keymap} from "@codemirror/view";

const $ = selector => document.querySelector(selector);
const fields = [...document.querySelectorAll("[data-field]")];
const sectionFields = new Set(["title", "publish", "description", "order", "banner", "bannerAlt"]);
const state = {
  path: new URLSearchParams(location.search).get("path") || "",
  kind: "article", mode: "visual", source: "", hash: "",
  bodyBaseline: "", formBaseline: {}, sourceChanged: false,
  filePath: "", fileHash: "", fileKind: "", fileSourceKind: "",
  fileDialogMode: "", dirty: false, busy: false, loadToken: 0,
};
const editable = new Compartment();
let editor;
let suppressChanges = false;

function status(message) { $("#status").textContent = message; }
function dirty(value = true) {
  state.dirty = value;
  document.title = value ? "* Obsite Editor" : "Obsite Editor";
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
    else input.value = value == null ? "" : String(value);
    input.closest("label").hidden = state.kind === "section" && !sectionFields.has(name);
  }
  state.formBaseline = formValues();
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
async function loadCatalog(preferred = state.path) {
  const data = await responseJSON(await fetch("/_obsite/sources"));
  const select = $("#source");
  select.replaceChildren();
  for (const source of data.sources) {
    const option = document.createElement("option");
    option.value = source.relPath;
    option.textContent = `${source.effectivePublish ? "" : "[Draft] "}${source.relPath}`;
    select.append(option);
  }
  select.value = data.sources.some(source => source.relPath === preferred) ? preferred : (data.sources[0]?.relPath || "");
  if (select.value) await loadSource(select.value);
  else clearEditor();
  await refreshFiles(preferred);
}
async function refreshFiles(preferred = state.filePath) {
  const data = await responseJSON(await fetch("/_obsite/files"));
  const tree = $("#file-tree");
  tree.replaceChildren();
  let selected = null;
  for (const entry of data.entries) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "file-entry";
    button.dataset.path = entry.path;
    button.setAttribute("role", "treeitem");
    button.setAttribute("aria-selected", String(entry.path === preferred || entry.path === state.filePath));
    button.style.paddingLeft = `${0.35 + entry.path.split("/").length * 0.7}rem`;
    const icon = document.createElement("span");
    icon.className = "file-entry-icon";
    icon.textContent = entry.kind === "folder" ? "▾" : "·";
    const label = document.createElement("span");
    label.className = "file-entry-label";
    label.textContent = entry.path;
    button.append(icon, label);
    button.onclick = () => selectFileEntry(entry);
    tree.append(button);
    if (entry.path === preferred || entry.path === state.filePath) selected = entry;
  }
  if (!data.entries.length) tree.textContent = "No managed files.";
  if (selected) {
    state.filePath = selected.path;
    state.fileHash = selected.hash;
    state.fileKind = selected.kind;
    state.fileSourceKind = selected.sourceKind || "";
  } else {
    state.filePath = "";
    state.fileHash = "";
    state.fileKind = "";
    state.fileSourceKind = "";
  }
  updateFileActions();
}
function selectFileEntry(entry) {
  if (entry.sourceKind && entry.path !== state.path && !confirmDiscard()) return;
  state.filePath = entry.path;
  state.fileHash = entry.hash;
  state.fileKind = entry.kind;
  state.fileSourceKind = entry.sourceKind || "";
  document.querySelectorAll(".file-entry").forEach(item => item.setAttribute("aria-selected", String(item.dataset.path === entry.path)));
  updateFileActions();
  if (entry.sourceKind) loadSource(entry.path);
  else status(`Selected ${entry.path}`);
}
function updateFileActions() {
  const hasSelection = Boolean(state.filePath) && !state.busy;
  $("#file-rename").disabled = !hasSelection;
  $("#file-delete").disabled = !hasSelection;
}
function clearEditor() {
  state.path = "";
  state.kind = "article";
  state.source = "";
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
    state.kind = data.kind;
    state.hash = data.sourceHash;
    state.filePath = path;
    state.fileHash = data.sourceHash;
    state.fileKind = "file";
    state.fileSourceKind = data.kind;
    state.source = data.source;
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
  } catch (error) { status(`Load failed: ${error.message}`); }
  finally { if (token === state.loadToken) busy(false); }
}
function updateMode() {
  $("#metadata").hidden = state.mode === "source";
  $("#mode").textContent = state.mode === "source" ? "Form mode" : "Source mode";
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
    status(label === "Save" ? "Save failed; see diagnostics." : `${label} failed; see diagnostics.`);
  }
  finally { busy(false); }
}
async function save() {
  await operation("Save", async () => {
    status("Validating, building, and saving…");
    const path = state.path;
    const mode = state.mode;
    const source = await compose();
    const result = await mutation("PUT", `/_obsite/source?path=${encodeURIComponent(path)}`, source, {"X-Obsite-Source-Hash": state.hash});
    dirty(false);
    await loadCatalog(path);
    if (mode === "source") { state.mode = "source"; setText(state.source); updateMode(); }
    diagnostics(result.diagnostics);
    status(result.warningCount ? "Saved and rebuilt with warnings." : "Saved and rebuilt.");
  });
}
async function preview() {
  await operation("Preview", async () => {
    status("Generating server-rendered draft preview…");
    const result = await mutation("POST", "/_obsite/preview", {path: state.path, source: await compose()});
    const frame = $("#preview-frame");
    frame.hidden = false;
    $("#preview-empty").hidden = true;
    frame.src = result.url;
    diagnostics(result.diagnostics);
    status(result.warningCount ? "Preview generated with warnings." : "Preview generated; source and public output were not changed.");
  });
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
function confirmDiscard() { return !state.dirty || confirm("There are unsaved changes. Discard them?"); }
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
function editorExtensions() {
  return [basicSetup, markdown(), EditorView.lineWrapping, editable.of(EditorView.editable.of(!state.busy)),
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
  try {
    const form = new FormData(); form.append("file", file);
    const result = await mutation("POST", "/_obsite/media", form);
    await loadMedia();
    insertMedia(result.path);
    status(`Uploaded ${result.path}; save the article to publish it.`);
  } catch (error) { status(`Upload failed: ${error.message}`); }
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
    status(`${label} failed; see diagnostics.`);
    throw error;
  } finally {
    busy(false);
  }
}

editor = new EditorView({state: EditorState.create({extensions: editorExtensions()}), parent: $("#editor")});
$("#source").onchange = event => {
  if (confirmDiscard()) loadSource(event.target.value);
  else event.target.value = state.path;
};
$("#refresh").onclick = () => { if (confirmDiscard()) loadCatalog().catch(error => status(error.message)); };
$("#save").onclick = save;
$("#preview").onclick = preview;
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
  const recursive = state.fileKind === "folder" ? " and all of its contents" : "";
  if (!confirm(`Delete ${state.filePath}${recursive}?`)) return;
  const pathValue = state.filePath;
  const fileHash = state.fileHash;
  const affectsCurrent = pathContains(pathValue, state.path);
  const preferred = affectsCurrent ? "" : state.path;
  runFileMutation("Delete", () => mutation("DELETE", `/_obsite/file?path=${encodeURIComponent(pathValue)}&confirm=true`, null, {"X-Obsite-File-Hash": fileHash}), preferred, affectsCurrent).catch(() => {});
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
  } catch (error) { diagnostics(error.diagnostics, error.message); $("#new-dialog").close(); status(`Create failed: ${error.message}`); }
};
$("#delete").onclick = () => operation("Delete", async () => {
  if (state.kind !== "article" || !confirm("Delete this article?")) return;
  await mutation("DELETE", `/_obsite/source?path=${encodeURIComponent(state.path)}&confirm=true`, null, {"X-Obsite-Source-Hash": state.hash});
  dirty(false); await loadCatalog(""); status("Deleted and rebuilt.");
});
$("#media").onclick = openMedia;
$("#media-close").onclick = () => { $("#media-panel").hidden = true; };
$("#media-upload").onchange = event => uploadMedia(event.target.files[0]);
document.querySelectorAll("[data-command]").forEach(button => {
  button.onmousedown = event => event.preventDefault();
  button.onclick = () => command(button.dataset.command);
});
window.addEventListener("beforeunload", event => { if (state.dirty) { event.preventDefault(); event.returnValue = ""; } });
loadCatalog().catch(error => status(`Editor unavailable: ${error.message}`));
