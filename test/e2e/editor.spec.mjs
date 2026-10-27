import {test, expect} from '@playwright/test';
import {spawn, execFileSync} from 'node:child_process';
import {createServer} from 'node:http';
import {promises as fs} from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const repoRoot = path.resolve(import.meta.dirname, '..', '..');
const passwordHash = '$argon2id$v=19$m=19456,t=2,p=1$o2KEDd/Nrt/G5QtUKhAY3w$9gLt3Rz/TkUAAJl6HSPSjjC1xbg3rAKuEq0AJQzyp5M';
let tempRoot;
let binaryPath;
let vault;
let child;
let origin;

async function freePort() {
  const server = createServer();
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const port = server.address().port;
  await new Promise(resolve => server.close(resolve));
  return port;
}

async function waitForHTTP(url) {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url);
      if (response.status < 500) return response;
    } catch {}
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw new Error(`timed out waiting for ${url}`);
}

async function waitForExit(process) {
  if (process.exitCode !== null) return;
  await new Promise(resolve => process.once('exit', resolve));
}

function shellQuote(value) {
  return `'${String(value).replaceAll("'", "'\\''")}'`;
}

async function login(page) {
  await page.goto(`${origin}/_oxpio/login`);
  await page.locator('input[name="username"]').fill('admin');
  await page.locator('input[name="password"]').fill('secret');
  await page.getByRole('button', {name: 'Log in'}).click();
  await expect(page).toHaveURL(/\/_oxpio\/editor$/);
  await expect(page.locator('body')).toContainText('Content editor');
}

async function session(context) {
  const response = await context.request.get(`${origin}/_oxpio/session`);
  expect(response.status()).toBe(200);
  return response.json();
}

async function source(context, relPath) {
  const response = await context.request.get(`${origin}/_oxpio/source?path=${encodeURIComponent(relPath)}`);
  return {
    response,
    body: await response.text(),
    hash: response.headers()['x-oxpio-source-hash'] || ''
  };
}

async function mutate(context, method, url, csrf, hash, data, contentType = '') {
  const headers = {
    Origin: origin,
    'X-OXPIO-CSRF': csrf,
    'X-OXPIO-Source-Hash': hash
  };
  if (contentType) headers['Content-Type'] = contentType;
  return context.request.fetch(`${origin}${url}`, {method, headers, data});
}

async function startConfiguredEditor() {
  vault = path.join(tempRoot, 'vault');
  await fs.mkdir(vault);
  await fs.writeFile(path.join(vault, 'oxpio.yaml'), `title: Editor E2E\nbaseURL: http://127.0.0.1/\nnavigation: []\neditor:\n  username: admin\n  passwordHash: ${passwordHash}\n`);
  await fs.writeFile(path.join(vault, '_index.md'), '---\ntitle: Home\npublish: true\n---\nHome\n');
  await fs.writeFile(path.join(vault, 'guide.md'), '---\ntitle: Guide\npublish: true\ntype: doc\n---\nGuide\n');
  await fs.mkdir(path.join(vault, 'section'));
  await fs.writeFile(path.join(vault, 'section', '_index.md'), '---\ntitle: Section\npublish: true\n---\nSection\n');
  await fs.writeFile(path.join(vault, 'article.md'), '---\n# preserve this comment\ntitle: "Article"\npublish: true\ntype: doc\n---\n\nOriginal\n');
  await fs.writeFile(path.join(vault, 'draft.md'), '---\ntitle: Draft\npublish: false\ntype: doc\n---\nPrivate\n\n$E = mc^2$\n\n```mermaid\ngraph TD\nA-->B\n```\n');
  const port = await freePort();
  origin = `http://127.0.0.1:${port}`;
  const configPath = path.join(vault, 'oxpio.yaml');
  const config = (await fs.readFile(configPath, 'utf8')).replace('http://127.0.0.1/', `${origin}/`);
  await fs.writeFile(configPath, config);
  child = spawn(binaryPath, ['editor', '--vault', vault, '--port', String(port)], {cwd: repoRoot, stdio: 'ignore'});
  await waitForHTTP(`${origin}/_oxpio/login`);
}

test.beforeEach(async () => {
  tempRoot = await fs.mkdtemp(path.join(os.tmpdir(), 'oxpio-edit-e2e-'));
  binaryPath = path.join(tempRoot, process.platform === 'win32' ? 'oxpio.exe' : 'oxpio');
  execFileSync('go', ['build', '-o', binaryPath, './cmd/oxpio'], {cwd: repoRoot});
  await startConfiguredEditor();
});

test.afterEach(async () => {
  if (child) {
    if (!child.killed) child.kill('SIGTERM');
    await waitForExit(child);
  }
  if (tempRoot) await fs.rm(tempRoot, {recursive: true, force: true});
  child = undefined;
});

test('anonymous public pages remain usable without JavaScript or external network', async ({browser}) => {
  const context = await browser.newContext({javaScriptEnabled: false});
  const blocked = [];
  await context.route('**/*', async route => {
    if (new URL(route.request().url()).origin === origin) await route.continue();
    else { blocked.push(route.request().url()); await route.abort('blockedbyclient'); }
  });
  const page = await context.newPage();
  await page.goto(`${origin}/article/`);
  await expect(page.locator('body')).toContainText('Original');
  await expect(page.locator('.edit-page-link')).toHaveCount(0);
  expect(blocked).toEqual([]);
  const sourceResponse = await context.request.get(`${origin}/_oxpio/source?path=article.md`);
  expect(sourceResponse.status()).toBe(401);
  await context.close();
});

test('new editor sessions select the home document after the initial catalog load', async ({browser}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_oxpio/editor`);
  await expect(page.locator('#content-editor')).toHaveAttribute('aria-busy', 'false');
  await expect(page.locator('#source')).toHaveValue('_index.md');
  await expect(page.locator('.source-tree-entry[data-path="_index.md"]')).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('#editor .cm-content')).toContainText('Home');
  await expect(page.locator('#save')).toBeEnabled();
  await context.close();
});

test('authenticated catalog maps articles, sections, drafts, and preserves full Markdown on save', async ({browser}) => {
  const context = await browser.newContext();
  const blocked = [];
  await context.route('**/*', async route => {
    if (new URL(route.request().url()).origin === origin) await route.continue();
    else { blocked.push(route.request().url()); await route.abort('blockedbyclient'); }
  });
  const page = await context.newPage();
  await page.goto(`${origin}/article/`);
  await expect(page.locator('.edit-page-link')).toHaveCount(0);
  await login(page);
  await expect(page.locator('.source-tree-folder')).toHaveAttribute('aria-expanded', 'false');
  await page.locator('#document-search').fill('Section');
  await expect(page.locator('#document-search-status')).toHaveText('1 of 5 documents');
  await expect(page.locator('.source-tree-folder')).toHaveAttribute('aria-expanded', 'true');
  await expect(page.locator('.source-tree-group[role="group"]')).toHaveCount(1);
  const sectionSource = page.locator('.source-tree-entry[data-path="section/_index.md"]');
  await expect(sectionSource).toBeVisible();
  await sectionSource.click();
  await expect(sectionSource).toHaveAttribute('aria-selected', 'true');
  await page.locator('#document-search').fill('');
  await expect(sectionSource).toBeVisible();
  await expect(page.locator('.source-tree-folder')).toHaveAttribute('aria-expanded', 'true');

  await page.goto(`${origin}/article/`);
  await expect(page.locator('.edit-page-link')).toHaveAttribute('href', /path=article\.md/);
  await page.goto(`${origin}/section/`);
  await expect(page.locator('.edit-page-link')).toHaveAttribute('href', /path=section%2F_index\.md/);
  await page.goto(`${origin}/_oxpio/editor?path=article.md`);
  await expect(page.locator('#editor .cm-content')).toContainText('Original');
  await page.locator('#source-mode').click();
  await expect(page.locator('#editor .cm-content')).toContainText('preserve this comment');
  const unchanged = await fs.readFile(path.join(vault, 'article.md'), 'utf8');
  await page.getByRole('button', {name: 'Save'}).click();
  await expect(page.locator('#status')).toContainText('Saved and rebuilt');
  expect(await fs.readFile(path.join(vault, 'article.md'), 'utf8')).toBe(unchanged);
  await expect(page.locator('#source option')).toHaveCount(5);
  await expect(page.locator('#source')).toContainText('[Draft] draft.md');

  const updated = '---\n# preserve this comment\ntitle: "Article"\npublish: true\ntype: doc\n---\n\nChanged from editor\n';
  await page.locator('#editor .cm-content').fill(updated);
  await page.getByRole('button', {name: 'Save'}).click();
  await expect(page.locator('#status')).toContainText('Saved and rebuilt');
  const saved = await fs.readFile(path.join(vault, 'article.md'), 'utf8');
  expect(saved).toBe(updated);
  await page.goto(`${origin}/article/`);
  await expect(page.locator('body')).toContainText('Changed from editor');
  expect(blocked).toEqual([]);
  await context.close();
});

test('editor announces initial catalog loading while controls are disabled', async ({browser}) => {
  const context = await browser.newContext();
  await context.route('**/_oxpio/sources', async route => {
    await new Promise(resolve => setTimeout(resolve, 1000));
    await route.continue();
  });
  const page = await context.newPage();
  await login(page);
  await expect(page.locator('#content-editor')).toHaveAttribute('aria-busy', 'true');
  await expect(page.locator('#busy-indicator')).toBeVisible();
  await expect(page.locator('#busy-message')).toHaveText('Loading document catalog…');
  await expect(page.locator('#save')).toBeDisabled();
  await expect(page.locator('#content-editor')).toHaveAttribute('aria-busy', 'false', {timeout: 20_000});
  await expect(page.locator('#status')).toContainText('Loaded');
  await context.close();
});

test('editor visual states hide irrelevant controls and protect frontmatter during media insertion', async ({browser}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_oxpio/editor?path=section/_index.md`);
  await expect(page.locator('#editor .cm-content')).toBeVisible();
  await expect(page.locator('.metadata-advanced')).toBeHidden();
  await page.goto(`${origin}/_oxpio/editor?path=article.md`);
  const originalSource = (await source(context, 'article.md')).body;
  const closingDelimiter = originalSource.indexOf('\n---\n');
  const originalFrontmatter = originalSource.slice(0, closingDelimiter + '\n---\n'.length);

  await expect(page.locator('.preview-pane')).toBeHidden();
  await expect(page.locator('#sync-scroll')).toHaveCount(0);
  await expect(page.locator('#preview-frame')).toBeHidden();
  await page.locator('#metadata-toggle').click();
  await expect(page.locator('#metadata-fields')).toBeHidden();
  await page.locator('#metadata-toggle').click();
  await expect(page.locator('#metadata-fields')).toBeVisible();
  await page.locator('#read-mode').click();
  await expect(page.locator('#preview-frame')).toBeVisible();
  await expect(page.locator('#preview-empty')).toBeHidden();
  const preview = page.frameLocator('#preview-frame');
  await expect(preview.locator('[data-page-content]')).toContainText('Original');
  await expect(preview.locator('.site-header')).toHaveCount(0);
  await expect(preview.locator('style[data-oxpio-basic-preview]')).toHaveCount(1);
  await expect(preview.locator('link[href*="style.css"]')).toHaveCount(0);
  await page.locator('#page-preview').click();
  await expect(page.frameLocator('#preview-frame').locator('.site-header')).toHaveCount(1);

  await page.locator('#file-new-folder').click();
  await expect(page.locator('#file-dialog')).toHaveAttribute('aria-labelledby', 'file-dialog-title');
  await expect(page.locator('#markdown-file-fields')).toBeHidden();
  await page.locator('#file-dialog').evaluate(dialog => dialog.close());
  await page.locator('#file-rename').click();
  await expect(page.locator('#markdown-file-fields')).toBeHidden();
  await page.locator('#file-dialog').evaluate(dialog => dialog.close());
  await page.locator('#new').click();
  await expect(page.locator('#new-dialog')).toBeVisible();
  await expect(page.locator('#new-dialog')).toHaveAttribute('aria-labelledby', 'new-dialog-title');
  await page.locator('#new-dialog').getByRole('button', {name: 'Cancel'}).click();
  await expect(page.locator('#new-dialog')).toBeHidden();
  await expect(page.locator('#new')).toBeFocused();

  await page.locator('#source-mode').click();
  await page.locator('#media').click();
  await page.locator('#media-upload').setInputFiles(path.join(repoRoot, 'test', 'testdata', 'e2e', 'runtime-vault', 'images', 'hero.png'));
  await page.waitForFunction(() => document.querySelectorAll('#media-list .media-item').length > 0);
  await expect.poll(async () => (await page.locator('#editor .cm-line').allTextContents()).join('\n')).toMatch(/^---[\s\S]*---\r?\n\r?\n!\[/);
  const editorSource = (await page.locator('#editor .cm-line').allTextContents()).join('\n');
  expect(editorSource.startsWith(`${originalFrontmatter}\n![hero](hero.png)`)).toBe(true);
  await page.getByRole('button', {name: 'Save'}).click();
  await expect(page.locator('#status')).toContainText('Saved and rebuilt');
  const persisted = await source(context, 'article.md');
  expect(persisted.body.startsWith(`${originalFrontmatter}\n![hero](hero.png)`)).toBe(true);
  await page.reload();
  await page.locator('#editor .cm-content').waitFor();
  await page.locator('#source-mode').click();
  await expect.poll(async () => (await page.locator('#editor .cm-line').allTextContents()).join('\n')).toContain('![hero](hero.png)');
  await context.close();
});

test('live preview renders unsaved editor changes in the same workspace', async ({browser}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  let previewRequests = 0;
  page.on('request', request => { if (new URL(request.url()).pathname.endsWith('/_oxpio/preview')) previewRequests += 1; });
  await login(page);
  await page.goto(`${origin}/_oxpio/editor?path=article.md`);
  const editor = page.locator('#editor .cm-content');
  await expect(editor).toBeVisible();
  await expect(page.locator('.preview-pane')).toBeHidden();
  await editor.fill('# Immediate save marker\n\nUnsaved before first preview');
  await expect.poll(() => previewRequests).toBe(0);
  await page.getByRole('button', {name: 'Save'}).click();
  await expect(page.locator('#status')).toContainText('Saved and rebuilt');
  await expect.poll(() => page.locator('#editor .live-rendered-block').count()).toBeGreaterThan(0);
  previewRequests = 0;
  await editor.fill('# Live preview marker\n\nInline math: $E = mc^2$.\n\n```go\nfunc Preview() {}\n```\n\n| Stage | Result |\n| --- | --- |\n| Draft | Ready |');
  await expect.poll(() => previewRequests).toBe(0);
  await expect.poll(() => page.locator('#editor .live-rendered-block').count()).toBeGreaterThan(0);
  const renderedBeforeHover = await page.locator('#editor .live-rendered-block').count();
  await page.locator('#editor .live-rendered-block').first().hover();
  await expect.poll(() => page.locator('#editor .live-rendered-block').count()).toBe(renderedBeforeHover);
  await expect.poll(async () => page.evaluate(() => {
    const html = [...document.querySelectorAll('#editor .live-rendered-block')].map(element => element.shadowRoot?.innerHTML || '').join('');
    return {math: html.includes('katex'), code: html.includes('<pre'), table: html.includes('<table')};
  })).toEqual({math: true, code: true, table: true});
  await page.getByRole('button', {name: 'Save'}).click();
  await expect(page.locator('#status')).toContainText('Saved and rebuilt');
  await expect.poll(() => page.locator('#editor .live-rendered-block').count()).toBeGreaterThan(0);
  await expect.poll(async () => page.evaluate(() => [...document.querySelectorAll('#editor .live-rendered-block')].some(element => element.shadowRoot?.textContent?.includes('Live preview marker')))).toBe(true);
  const liveBlock = page.locator('#editor .live-rendered-block').first();
  const renderedBeforeClick = await page.locator('#editor .live-rendered-block').count();
  await liveBlock.click();
  await expect(page.locator('#write-mode')).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('#source-mode')).toHaveAttribute('aria-selected', 'false');
  await expect(page.locator('#editor .cm-content')).toContainText('# Live preview marker');
  await expect(page.locator('#editor .live-rendered-block')).toHaveCount(renderedBeforeClick - 1);
  await page.locator('#editor .cm-content').press('X');
  await expect(page.locator('#editor .cm-content')).toContainText('X# Live preview marker');
  await expect(page.locator('#editor .cm-content')).not.toContainText('X---');
  await expect(page.locator('.preview-pane')).toBeHidden();
  await context.close();
});

test('write mode reveals only the active inline Markdown node', async ({browser}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_oxpio/editor?path=article.md`);
  const editor = page.locator('#editor .cm-content');
  await editor.fill('Normal **bold** and *italic* with [a link](https://example.test).\n\nNeighbor block');
  await expect.poll(() => page.locator('#editor .live-rendered-block').count()).toBeGreaterThan(0);
  await expect.poll(async () => page.evaluate(() => document.querySelector('#editor .live-rendered-block')?.shadowRoot?.innerHTML || '')).toContain('data-live-inline-from');
  const firstBlock = page.locator('#editor .live-rendered-block').first();
  await firstBlock.locator('strong').click();
  await expect(page.locator('#write-mode')).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('#source-mode')).toHaveAttribute('aria-selected', 'false');
  await expect(editor).toContainText('**bold**');
  await expect(editor).not.toContainText('*italic*');
  await expect(editor).not.toContainText('](https://example.test)');
  await expect(page.locator('#editor .live-rendered-block')).toHaveCount(1);
  const inactiveInline = page.locator('#editor .live-rendered-inline').first();
  await expect(inactiveInline).toBeVisible();
  await inactiveInline.click();
  await expect(editor).toContainText('*italic*');
  await expect(editor).not.toContainText('**bold**');
  const inactiveLink = page.locator('#editor .live-rendered-inline[data-live-inline-name="Link"]');
  await expect(inactiveLink).toBeVisible();
  await inactiveLink.click();
  await expect(editor).toContainText('[a link](https://example.test)');
  await expect(editor).not.toContainText('**bold**');
  await expect(editor).not.toContainText('*italic*');
  await context.close();
});

test('footnote references reveal their local Markdown source', async ({browser}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_oxpio/editor?path=article.md`);
  const editor = page.locator('#editor .cm-content');
  await editor.fill('Text[^1]\n\n[^1]: Note\n\nNeighbor\n');
  const reference = page.locator('#editor .live-rendered-block [data-live-inline-name="Link"]');
  await expect.poll(() => reference.count()).toBeGreaterThan(0);
  await reference.first().click();
  await expect(editor).toContainText('[^1]');
  await context.close();
});

test('math live preview renders KaTeX and exposes only the active formula source', async ({browser}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_oxpio/editor?path=article.md`);
  const editor = page.locator('#editor .cm-content');
  await expect(editor).toHaveAttribute('contenteditable', 'true');
  await editor.fill('前置 **bold** $x^2$ 和 \\(y^2\\) 后。\n\nNeighbor block');
  await expect(page.locator('#editor .oxpio-math')).toHaveCount(2);
  await expect(page.locator('#editor .oxpio-math .katex')).toHaveCount(2);
  await page.locator('#editor .oxpio-math').last().click();
  await expect(editor).toContainText('\\(y^2\\)');
  await expect(editor).not.toContainText('$x^2$');
  await expect.poll(() => page.locator('#editor .live-rendered-block').count()).toBe(1);
  await context.close();
});

test('write preview keeps layout stable and exposes local render metrics', async ({browser}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_oxpio/editor?path=article.md`);
  const editor = page.locator('#editor .cm-content');
  const toolbarBox = await page.locator('.toolbar').boundingBox();
  const editorPaneBox = await page.locator('.editor-pane').boundingBox();
  expect(Math.abs((toolbarBox.y + toolbarBox.height) - editorPaneBox.y)).toBeLessThan(0.5);
  await page.evaluate(() => {
    window.__oxpioLayoutShifts = 0;
    if (window.PerformanceObserver) new PerformanceObserver(list => { window.__oxpioLayoutShifts += list.getEntries().filter(entry => !entry.hadRecentInput).reduce((sum, entry) => sum + entry.value, 0); }).observe({type: 'layout-shift', buffered: true});
  });
  const before = await page.evaluate(() => ({busy: document.querySelector('#busy-indicator')?.getBoundingClientRect().height, editorTop: document.querySelector('#content-editor')?.getBoundingClientRect().top}));
  await editor.fill('A '.repeat(20) + '**local** preview');
  await expect.poll(() => page.evaluate(() => window.__oxpioLivePreviewMetrics?.coreRenders || 0)).toBeGreaterThan(0);
  await expect.poll(() => page.evaluate(() => window.__oxpioLivePreviewMetrics?.keyToPaintMs?.length || 0)).toBeGreaterThan(0);
  await expect.poll(() => page.evaluate(() => window.__oxpioLivePreviewMetrics?.serviceRequests || 0)).toBe(0);
  const after = await page.evaluate(() => ({busy: document.querySelector('#busy-indicator')?.getBoundingClientRect().height, editorTop: document.querySelector('#content-editor')?.getBoundingClientRect().top}));
  expect(after.busy).toBe(before.busy);
  expect(after.editorTop).toBe(before.editorTop);
  expect(await page.evaluate(() => window.__oxpioLayoutShifts || 0)).toBeLessThan(0.001);
  await context.close();
});

test('rapid Write input coalesces local worker tasks without service preview requests', async ({browser}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_oxpio/editor?path=article.md`);
  const editor = page.locator('#editor .cm-content');
  await expect.poll(() => page.evaluate(() => window.__oxpioLivePreviewMetrics?.coreRenders || 0)).toBeGreaterThan(0);
  const before = await page.evaluate(() => window.__oxpioLivePreviewMetrics?.workerTasks || 0);
  await editor.click();
  await editor.press('Control+End');
  await editor.pressSequentially('abcdefghijklmnopqrst', {delay: 0});
  await expect.poll(() => page.evaluate(value => (window.__oxpioLivePreviewMetrics?.workerTasks || 0) > value, before)).toBe(true);
  const after = await page.evaluate(() => ({workerTasks: window.__oxpioLivePreviewMetrics?.workerTasks || 0, serviceRequests: window.__oxpioLivePreviewMetrics?.serviceRequests || 0}));
  expect(after.workerTasks - before).toBeLessThan(20);
  expect(after.serviceRequests).toBe(0);
  await expect(editor).toContainText('abcdefghijklmnopqrst');
  await context.close();
});

test('unchanged live blocks reuse their rendered widgets after local edits', async ({browser}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_oxpio/editor?path=article.md`);
  const editor = page.locator('#editor .cm-content');
  await editor.fill('First **block**\n\nSecond *block*\n\nThird [block](https://example.test)');
  await expect.poll(() => page.locator('#editor .live-rendered-block').count()).toBeGreaterThan(1);
  await page.evaluate(() => { window.__oxpioFirstLiveBlock = document.querySelector('#editor .live-rendered-block'); });
  const before = await page.evaluate(() => window.__oxpioLivePreviewMetrics?.coreRenders || 0);
  await editor.press('End');
  await editor.press('x');
  await expect.poll(() => page.evaluate(value => (window.__oxpioLivePreviewMetrics?.coreRenders || 0) > value, before)).toBe(true);
  await expect.poll(() => page.evaluate(() => window.__oxpioFirstLiveBlock === document.querySelector('#editor .live-rendered-block'))).toBe(true);
  await context.close();
});

test('long Write documents update locally without service preview requests', async ({browser}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_oxpio/editor?path=article.md`);
  const editor = page.locator('#editor .cm-content');
  const longDocument = Array.from({length: 180}, (_, index) => `## Section ${index}\n\nParagraph ${index} with **strong** and [link](https://example.test/${index}).`).join('\n\n');
  const started = Date.now();
  await editor.fill(longDocument);
  await expect.poll(() => page.evaluate(() => window.__oxpioLivePreviewMetrics?.coreRenders || 0)).toBeGreaterThan(0);
  expect(Date.now() - started).toBeLessThan(5000);
  await expect.poll(() => page.evaluate(() => window.__oxpioLivePreviewMetrics?.serviceRequests || 0)).toBe(0);
  await context.close();
});

test('live preview fixture uses shared semantic rendering and local high-cost runtimes', async ({browser}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_oxpio/editor?path=article.md`);
  const editor = page.locator('#editor .cm-content');
  await editor.fill(await fs.readFile(path.join(repoRoot, 'test', 'testdata', 'editor', 'live-preview.md'), 'utf8'));
  await expect.poll(() => page.evaluate(() => window.__oxpioLivePreviewMetrics?.coreRenders || 0)).toBeGreaterThan(0);
  const renderedHTML = () => page.evaluate(() => [...document.querySelectorAll('#editor .live-rendered-block')].map(element => element.shadowRoot?.innerHTML || '').join(''));
  const mermaidBlocks = new Set();
  const scrollPositions = [];
  let mathHTML = '';
  let calloutHTML = '';
  let mermaidErrorFound = false;
  let katexErrorFound = false;
  for (const fraction of Array.from({length: 21}, (_, index) => index / 20)) {
    const scrollState = await page.evaluate(value => {
      const maxScroll = Math.max(0, document.documentElement.scrollHeight - window.innerHeight);
      window.scrollTo(0, maxScroll * value);
      return {maxScroll, scrollY: window.scrollY};
    }, fraction);
    scrollPositions.push(scrollState.scrollY);
    await page.waitForTimeout(150);
    const html = await renderedHTML();
    const visibleState = await page.evaluate(() => {
      const blocks = [...document.querySelectorAll('#editor .live-rendered-block')];
      const mathBlocks = blocks.filter(element => element.shadowRoot?.querySelector('[data-live-inline-name="Math"]'));
      return {
        mathMounted: mathBlocks.length > 0,
        mathRendered: mathBlocks.some(element => element.shadowRoot?.querySelector('[data-live-inline-name="Math"] .katex')),
        katexError: mathBlocks.some(element => element.shadowRoot?.querySelector('.katex-error')),
        mermaidError: blocks.some(element => element.shadowRoot?.querySelector('svg[aria-roledescription="error"]')),
        normalWhitespace: blocks.every(element => getComputedStyle(element).whiteSpace === 'normal'),
      };
    });
    mermaidErrorFound ||= visibleState.mermaidError;
    katexErrorFound ||= visibleState.katexError;
    expect(visibleState.normalWhitespace).toBe(true);
    if (visibleState.mathMounted) {
      await expect.poll(() => page.evaluate(() => [...document.querySelectorAll('#editor .live-rendered-block')].some(element => element.shadowRoot?.querySelector('[data-live-inline-name="Math"] .katex')))).toBe(true);
      mathHTML = await renderedHTML();
    }
    if (html.includes('callout-note')) calloutHTML = html;
    const mounted = await page.evaluate(() => [...document.querySelectorAll('#editor .live-rendered-block')].filter(element => element.shadowRoot?.querySelector('pre.mermaid svg')).map(element => element.dataset.liveBlockFrom));
    mounted.forEach(from => mermaidBlocks.add(from));
  }
  expect(Math.max(...scrollPositions) - Math.min(...scrollPositions)).toBeGreaterThan(0);
  expect(mathHTML).toContain('data-live-inline-name="Math"');
  expect(mathHTML).toContain('<span class="katex">');
  expect(katexErrorFound).toBe(false);
  expect(mermaidBlocks.size).toBe(2);
  expect(calloutHTML).toContain('callout-note');
  expect(mermaidErrorFound).toBe(false);
  await page.screenshot({path: path.join(repoRoot, 'test-results', 'live-preview-syntax-gallery-top.png'), fullPage: true});
  await context.close();
});

test('task status gallery uses Obsidian-style semantic SVG markers', async ({browser}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_oxpio/editor?path=article.md`);
  const editor = page.locator('#editor .cm-content');
  await editor.fill(`## Basic\n- [ ] to-do\n- [/] incomplete\n- [x] done\n- [-] canceled\n- [>] forwarded\n- [<] scheduling\n\n## Extras\n- [?] question\n- [!] important\n- [*] star\n- [\"] quote\n- [l] location\n- [b] bookmark\n- [i] information\n- [S] savings\n- [I] idea\n- [p] pros\n- [c] cons\n- [f] fire\n- [k] key\n- [w] win\n- [u] up\n- [d] down\n- [D] draft pull request\n- [P] open pull request\n- [M] merged pull request`);
  await page.getByText('Basic', {exact: true}).click();
  const renderedHTML = () => page.evaluate(() => [...document.querySelectorAll('#editor .live-rendered-block')].map(element => element.shadowRoot?.innerHTML || '').join(''));
  await expect.poll(renderedHTML).toContain('data-task=\"?\"');
  const html = await renderedHTML();
  for (const status of ['data-task=\" \"', 'data-task=\"/\"', 'data-task=\"x\"', 'data-task=\"-\"', 'data-task=\"&gt;\"', 'data-task=\"&lt;\"', 'data-task=\"?\"', 'data-task=\"M\"']) expect(html).toContain(status);
  expect(html).toContain('oxpio-task-input');
  await page.screenshot({path: path.join(repoRoot, 'test-results', 'task-status-gallery.png'), fullPage: true});
  await context.close();
});

test('source media insertion preserves mixed line endings after save and reload', async ({browser}) => {
  const mixedSource = '---\r\n# preserve this comment\r\ntitle: "Article"\r\npublish: true\r\ntype: doc\r\n---\r\n\nOriginal\n';
  await fs.writeFile(path.join(vault, 'article.md'), mixedSource);
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_oxpio/editor?path=article.md`);
  const delimiter = mixedSource.indexOf('\r\n---\r\n') + '\r\n---\r\n'.length;
  const originalFrontmatter = mixedSource.slice(0, delimiter);
  const expected = `${originalFrontmatter}\r\n![hero](hero.png)\r\n\nOriginal\n`;
  await page.locator('#source-mode').click();
  await page.locator('#media').click();
  await page.locator('#media-upload').setInputFiles(path.join(repoRoot, 'test', 'testdata', 'e2e', 'runtime-vault', 'images', 'hero.png'));
  await page.waitForFunction(() => document.querySelectorAll('#media-list .media-item').length > 0);
  await page.getByRole('button', {name: 'Save'}).click();
  await expect(page.locator('#status')).toContainText('Saved and rebuilt');
  expect((await source(context, 'article.md')).body).toBe(expected);
  await page.reload();
  expect((await source(context, 'article.md')).body).toBe(expected);
  await context.close();
});

test('form metadata edits preserve mixed source bytes after save and reload', async ({browser}) => {
  const original = '---\r\n# preserve this comment\r\ntitle: "Article"\r\npublish: true\r\ntype: doc\r\n---\r\n\nOriginal\n';
  await fs.writeFile(path.join(vault, 'article.md'), original);
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_oxpio/editor?path=article.md`);
  await page.locator('[data-field="title"]').fill('Edited');
  await page.getByRole('button', {name: 'Save'}).click();
  await expect(page.locator('#status')).toContainText('Saved and rebuilt');
  const expected = original.replace('title: "Article"\r\n', 'title: Edited\r\n');
  expect((await source(context, 'article.md')).body).toBe(expected);
  await page.reload();
  expect((await source(context, 'article.md')).body).toBe(expected);
  await context.close();
});

test('editor dark mode keeps metadata and long file labels readable', async ({browser}) => {
  const longPath = 'deeply-nested-folder-with-a-long-name/another-long-folder/article-with-a-long-name.md';
  await fs.mkdir(path.dirname(path.join(vault, longPath)), {recursive: true});
  await fs.writeFile(path.join(vault, longPath), '---\ntitle: Long\npublish: false\ntype: doc\n---\nLong\n');
  const context = await browser.newContext({viewport: {width: 390, height: 844}, colorScheme: 'dark'});
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_oxpio/editor?path=article.md`);
  await page.locator('#editor .cm-content').fill('Dark **preview**\n\nNeighbor');
  await expect.poll(() => page.locator('#editor .live-rendered-block').count()).toBeGreaterThan(0);
  await page.locator('#sidebar-toggle').click();
  await page.locator('#file-refresh').click();
  const label = page.locator('.file-entry-label').filter({hasText: longPath}).first();
  await expect(label).toBeVisible();
  const labelLayout = await label.evaluate(node => {
    const rect = node.getBoundingClientRect();
    const parent = node.parentElement.getBoundingClientRect();
    const style = getComputedStyle(node);
    return {contained: rect.right <= parent.right + 1, overflow: style.overflow, textOverflow: style.textOverflow};
  });
  expect(labelLayout.contained).toBe(true);
  expect(labelLayout.overflow).toBe('hidden');
  expect(labelLayout.textOverflow).toBe('ellipsis');
  await expect(page.locator('.metadata-advanced summary')).toHaveCSS('color', 'rgb(200, 212, 223)');
  await expect(page.locator('#document-search-status')).toHaveCSS('color', 'rgb(200, 212, 223)');
  await expect.poll(() => page.evaluate(() => {
    const block = document.querySelector('#editor .live-rendered-block');
    const content = block?.shadowRoot?.querySelector('.content-preview-content');
    return content ? getComputedStyle(content).color : '';
  })).toBe('rgb(232, 238, 241)');
  await context.close();
});

test('editor mobile layout keeps the workspace ahead of navigation', async ({browser}) => {
  const context = await browser.newContext({viewport: {width: 390, height: 844}});
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_oxpio/editor?path=article.md`);
  await page.locator('#editor .cm-content').waitFor();
  await expect(page.locator('.metadata')).toBeVisible();
  await expect(page.locator('.toolbar')).toBeVisible();
  await expect(page.locator('#settings')).toBeVisible();
  await expect(page.locator('#settings')).toHaveAttribute('aria-label', 'Editor settings');
  await page.locator('#settings').click();
  await expect(page.locator('#settings-dialog')).toBeVisible();
  await expect(page.locator('#settings-dialog')).toHaveAttribute('aria-labelledby', 'settings-dialog-title');
  await page.locator('#settings-dialog button[value="cancel"]').click();
  const layout = await page.evaluate(() => {
    const workspace = document.querySelector('.workspace').getBoundingClientRect();
    const sidebar = document.querySelector('.source-sidebar').getBoundingClientRect();
    const editor = document.querySelector('#editor').getBoundingClientRect();
    const editorGrid = document.querySelector('.editor-preview-grid');
    const pane = document.querySelector('.editor-pane');
    return {
      workspaceTop: workspace.top,
      sidebarTop: sidebar.top,
      editorWidth: editor.width,
      viewportWidth: innerWidth,
      editorGridMinHeight: parseFloat(getComputedStyle(editorGrid).minHeight),
      paneMinHeight: parseFloat(getComputedStyle(pane).minHeight),
    };
  });
  expect(layout.workspaceTop).toBeLessThan(layout.sidebarTop);
  expect(layout.editorWidth).toBeLessThanOrEqual(layout.viewportWidth);
  expect(layout.editorGridMinHeight).toBeLessThan(608);
  expect(layout.paneMinHeight).toBeLessThan(608);
  await context.close();
});

test('browser create defaults to a private draft, rejects bad posts, and deletes after confirmation', async ({browser}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_oxpio/editor?path=article.md`);

  await page.locator('#new').click();
  await page.locator('#new-dialog input[name="path"]').fill('new-browser.md');
  await page.locator('#new-dialog input[name="title"]').fill('Browser draft');
  await page.locator('#new-dialog select[name="type"]').selectOption('doc');
  await page.locator('#new-submit').click();
  await expect.poll(async () => (await fs.access(path.join(vault, 'new-browser.md')).then(() => true).catch(() => false))).toBe(true);
  const draft = await fs.readFile(path.join(vault, 'new-browser.md'), 'utf8');
  expect(draft).toBe('---\ntitle: "Browser draft"\npublish: false\ntype: doc\n---\n\n');
  const draftPage = await context.request.get(`${origin}/new-browser/`);
  expect(draftPage.status()).toBe(404);
  expect(await draftPage.text()).not.toContain('Browser draft');

  await page.goto(`${origin}/_oxpio/editor?path=new-browser.md`);
  page.once('dialog', dialog => dialog.accept());
  await page.locator('#delete').click();
  await expect.poll(async () => (await fs.access(path.join(vault, 'new-browser.md')).then(() => true).catch(() => false))).toBe(false);
  expect((await context.request.get(`${origin}/new-browser/`)).status()).toBe(404);

  const csrf = (await session(context)).csrf;
  const badPost = await mutate(context, 'POST', '/_oxpio/source', csrf, 'absent', new URLSearchParams({path: 'missing-date.md', title: 'Bad post', type: 'post'}).toString(), 'application/x-www-form-urlencoded');
  expect(badPost.status()).toBe(400);
  expect(await badPost.text()).toContain('date is required');
  await context.close();
});

test('failed publishes and stale hashes are visible and leave source and output unchanged', async ({browser}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  const before = await source(context, 'article.md');
  const beforePage = await context.request.get(`${origin}/article/`);
  const beforeHTML = await beforePage.text();

  await page.goto(`${origin}/_oxpio/editor?path=article.md`);
  await page.locator('#source-mode').click();
  await page.locator('#editor .cm-content').fill('---\ntitle: Broken\npublish: true\ntype: invalid\n---\nFailure\n');
  await page.getByRole('button', {name: 'Save'}).click();
  await expect(page.locator('#status')).toContainText('Save failed');
  const afterFailure = await source(context, 'article.md');
  expect(afterFailure.body).toBe(before.body);
  expect((await (await context.request.get(`${origin}/article/`)).text())).toBe(beforeHTML);

  const external = '---\ntitle: External\npublish: true\ntype: doc\n---\nExternal\n';
  await fs.writeFile(path.join(vault, 'article.md'), external);
  const stale = await mutate(context, 'PUT', '/_oxpio/source?path=article.md', (await session(context)).csrf, before.hash, 'stale');
  expect(stale.status()).toBe(409);
  expect(await stale.text()).toContain('source conflict');
  expect(await fs.readFile(path.join(vault, 'article.md'), 'utf8')).toBe(external);
  await context.close();
});

test('successful edit sends one live reload and serve remains read-only', async ({browser}) => {
  const context = await browser.newContext();
  const publicPage = await context.newPage();
  await publicPage.goto(`${origin}/article/`);
  await expect(publicPage.locator('body')).toContainText('Original');

  const editorPage = await context.newPage();
  await login(editorPage);
  await editorPage.goto(`${origin}/_oxpio/editor?path=article.md`);
  await editorPage.locator('#source-mode').click();
  await editorPage.locator('#editor .cm-content').fill('---\ntitle: Article\npublish: true\ntype: doc\n---\nReloaded\n');
  const navigations = [];
  publicPage.on('framenavigated', frame => {
    if (frame === publicPage.mainFrame()) navigations.push(frame.url());
  });
  await editorPage.getByRole('button', {name: 'Save'}).click();
  await expect(editorPage.locator('#status')).toContainText('Saved and rebuilt');
  await expect.poll(() => navigations.length, {timeout: 5_000}).toBe(1);
  await expect(publicPage.locator('body')).toContainText('Reloaded');

  const servePort = await freePort();
  const serveOrigin = `http://127.0.0.1:${servePort}`;
  const readOnly = spawn(binaryPath, ['serve', '--vault', vault, '--output', path.join(vault, 'public'), '--port', String(servePort)], {cwd: repoRoot, stdio: 'ignore'});
  try {
    await waitForHTTP(`${serveOrigin}/article/`);
    const control = await fetch(`${serveOrigin}/_oxpio/login`);
    expect(control.status).toBe(404);
    const sourceResponse = await fetch(`${serveOrigin}/_oxpio/source?path=article.md`);
    expect(sourceResponse.status).toBe(404);
    const servedPage = await (await fetch(`${serveOrigin}/article/`)).text();
    expect(servedPage).not.toContain('edit-page-link');
  } finally {
    if (!readOnly.killed) readOnly.kill('SIGTERM');
    await waitForExit(readOnly);
  }
  await context.close();
});

test('structured editor exposes metadata forms, draft preview, and media management', async ({browser}) => {
  const context = await browser.newContext();
  const externalRequests = [];
  context.on('request', request => {
    if (new URL(request.url()).origin !== origin) externalRequests.push(request.url());
  });
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_oxpio/editor?path=draft.md`);
  await expect(page.locator('[data-field="title"]')).toHaveValue('Draft');
  await expect(page.locator('[data-field="date"]')).toHaveAttribute('type', 'date');
  await expect(page.locator('[data-field="updated"]')).toHaveAttribute('type', 'date');
  await expect(page.locator('#editor .cm-content')).toContainText('Private');
  await expect(page.locator('#editor .cm-content')).not.toContainText('title: Draft');

  await page.locator('#read-mode').click();
  await expect(page.locator('#preview-frame')).not.toBeHidden();
  const draftPreview = page.frameLocator('#preview-frame');
  await expect(draftPreview.locator('body')).toContainText('Private');
  await expect(draftPreview.locator('html')).toHaveAttribute('data-oxpio-math', '');
  await expect(draftPreview.locator('html')).toHaveAttribute('data-oxpio-mermaid', '');
  await expect.poll(async () => draftPreview.locator('.katex').count()).toBeGreaterThan(0);
  await expect.poll(async () => draftPreview.locator('svg').count()).toBeGreaterThan(0);
  const contentPreviewResponse = await context.request.get(`${origin}${await page.locator('#preview-frame').getAttribute('src')}`);
  expect(contentPreviewResponse.headers()['cache-control']).toBe('no-store');
  await page.locator('#write-mode').click();
  await expect(page.locator('[data-field="title"]')).toBeVisible();
  await page.locator('#editor .cm-content').fill('> [!NOTE] Preview note\n> Note body\n\n- [x] Done\n- [?] Question\n');
  await page.locator('#read-mode').click();
  const semanticPreview = page.frameLocator('#preview-frame');
  await expect(semanticPreview.locator('style[data-oxpio-basic-preview]')).toHaveCount(1);
  await expect(semanticPreview.locator('.callout')).toHaveCount(1);
  await expect(semanticPreview.locator('.callout-title')).toHaveText('Preview note');
  await expect(semanticPreview.locator('.oxpio-task-input')).toHaveCount(2);
  await expect(semanticPreview.locator('.oxpio-task-input').first()).toBeChecked();
  await expect(semanticPreview.locator('.oxpio-task-input').nth(1)).toBeChecked();
  const previewTaskStyle = await semanticPreview.locator('.oxpio-task-input').first().evaluate(element => ({backgroundImage: getComputedStyle(element).backgroundImage, boxShadow: getComputedStyle(element).boxShadow}));
  expect(previewTaskStyle.backgroundImage).toContain('data:image/svg+xml');
  expect(previewTaskStyle.boxShadow).toBe('none');
  await expect(semanticPreview.locator('li').filter({hasText: 'Question'})).toHaveCSS('text-decoration-line', 'none');
  await page.locator('#write-mode').click();

  await page.locator('[data-field="title"]').fill('Updated draft');
  await page.locator('[data-field="description"]').fill('A draft description');
  await page.locator('[data-field="date"]').fill('2026-04-10');
  await page.locator('[data-field="updated"]').fill('2026-04-11');
  await page.getByRole('button', {name: 'Save'}).click();
  await expect(page.locator('#status')).toContainText('Saved and rebuilt');
  const saved = await fs.readFile(path.join(vault, 'draft.md'), 'utf8');
  expect(saved).toContain('title: Updated draft');
  expect(saved).toContain('description: A draft description');
  expect(saved).toMatch(/date: ["']?2026-04-10["']?/);
  expect(saved).toMatch(/updated: ["']?2026-04-11["']?/);
  expect(saved).toContain('publish: false');

  await page.getByRole('button', {name: 'Media library'}).click();
  const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=', 'base64');
  await page.evaluate(bytes => {
    const transfer = new DataTransfer();
    transfer.items.add(new File([new Uint8Array(bytes)], 'editor-drop.png', {type: 'image/png'}));
    document.querySelector('#media-dropzone').dispatchEvent(new DragEvent('drop', {bubbles: true, dataTransfer: transfer}));
  }, [...png]);
  await expect(page.locator('.media-item')).toContainText('editor-drop.png');
  await page.locator('#media-upload').setInputFiles({name: 'editor-pixel.png', mimeType: 'image/png', buffer: png});
  await expect(page.locator('.media-item').filter({hasText: 'editor-pixel.png'})).toHaveCount(1);
  await expect(page.locator('#status')).toContainText('Uploaded editor-pixel.png');
  await page.getByRole('button', {name: 'Save'}).click();
  await expect(page.locator('#status')).toContainText('Saved and rebuilt');
  const mediaSaved = await fs.readFile(path.join(vault, 'draft.md'), 'utf8');
  expect(mediaSaved).toContain('editor-pixel.png');

  await page.locator('#read-mode').click();
  const contentPreview = page.frameLocator('#preview-frame');
  await expect(contentPreview.locator('[data-page-content] img')).toHaveCount(2);
  await page.getByRole('button', {name: 'New folder'}).click();
  await page.locator('#file-path').fill('docs');
  await page.locator('#file-submit').click();
  await expect(page.locator('.file-entry[data-path="docs"]')).toHaveCount(1);

  const orphanMarkdown = Buffer.from('---\ntitle: Orphan\npublish: false\ntype: doc\n---\nOrphan\n');
  await page.locator('#write-mode').click();
  await page.getByRole('button', {name: 'Media library'}).click();
  await page.locator('#upload-folder').fill('docs');
  await page.locator('#media-upload').setInputFiles({name: 'orphan.md', mimeType: 'text/markdown', buffer: orphanMarkdown});
  await expect(page.locator('#upload-parent-warning')).toContainText('docs/_index.md');
  await expect(page.locator('.file-entry[data-path="docs/orphan.md"]')).toHaveCount(0);
  await page.locator('#read-mode').click();

  await page.getByRole('button', {name: 'New', exact: true}).click();
  await page.locator('#new-form input[name="path"]').fill('docs/top-level.md');
  await expect(page.locator('#new-parent-warning')).toContainText('docs/_index.md');
  await expect(page.locator('#new-submit')).toBeDisabled();
  await page.locator('#new-dialog').evaluate(dialog => dialog.close());

  await page.getByRole('button', {name: 'New Markdown'}).click();
  await page.locator('#file-path').fill('docs/guide.md');
  await expect(page.locator('#file-parent-warning')).toContainText('docs/_index.md');
  await expect(page.locator('#file-submit')).toBeDisabled();
  await page.locator('#file-dialog').evaluate(dialog => dialog.close());

  await page.getByRole('button', {name: 'New Markdown'}).click();
  await page.locator('#file-path').fill('docs/_INDEX.MD');
  await expect(page.locator('#file-parent-warning')).toHaveText('Section index files must use the exact filename _index.md.');
  await expect(page.locator('#file-submit')).toBeDisabled();
  await page.locator('#file-path').fill('docs/_index.md');
  await expect(page.locator('#file-parent-warning')).toHaveText('Files named _index.md must use Section index kind.');
  await expect(page.locator('#file-submit')).toBeDisabled();
  await page.locator('#file-title').fill('Docs');
  await page.locator('#file-kind').selectOption('section-index');
  await expect(page.locator('#file-submit')).toBeEnabled();
  await page.locator('#file-submit').click();
  await expect(page.locator('.file-entry[data-path="docs/_index.md"]')).toHaveCount(1);

  await page.getByRole('button', {name: 'New Markdown'}).click();
  await page.locator('#file-path').fill('docs/guide.md');
  await page.locator('#file-title').fill('Guide');
  await page.locator('#file-submit').click();
  await expect(page.locator('.file-entry[data-path="docs/guide.md"]')).toHaveCount(1);

  await page.getByRole('button', {name: 'Rename'}).click();
  await page.locator('#file-path').fill('docs/renamed.md');
  await page.locator('#file-submit').click();
  await expect(page.locator('.file-entry[data-path="docs/renamed.md"]')).toHaveCount(1);

  page.once('dialog', dialog => dialog.accept());
  await page.locator('#file-delete').click();
  await expect(page.locator('.file-entry[data-path="docs/renamed.md"]')).toHaveCount(0);
  await page.locator('.file-entry[data-path="docs"]').click();
  await page.locator('.file-entry[data-path="docs"]').click();
  await expect(page.locator('#file-delete')).toBeDisabled();
  expect(externalRequests).toEqual([]);
  await context.close();
});

test('preview diff uses the bundled merge view and local drafts never publish', async ({browser}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_oxpio/editor?path=article.md`);
  await expect(page.locator('#editor .cm-content')).toBeVisible();
  await page.locator('#source-mode').click();
  await page.locator('#editor .cm-content').fill('---\ntitle: Article\npublish: true\ntype: doc\ntags:\n- local\n---\nLocal candidate\n');
  await page.locator('#diff-mode').click();
  await expect(page.locator('#diff-editor')).toBeVisible();
  await expect(page.locator('.diff-summary')).toContainText('article.md');
  await expect(page.locator('#diff-editor .merge-host')).toBeVisible();
  await expect(page.locator('#diff-mode')).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('#diff-wrap')).toBeVisible();
  await page.locator('#diff-wrap').click();
  await expect(page.locator('#diff-wrap')).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('#diff-editor .cm-content').last()).toContainText('Local candidate');
  await expect(page.locator('#diff-editor')).not.toContainText('Rendered Markdown');
  await expect(page.locator('#diff-editor')).not.toContainText('Rendered changed blocks');
  await expect(page.locator('#diff-editor')).not.toContainText('Frontmatter fields');
  await expect(page.locator('#diff-editor')).not.toContainText('Markdown blocks');
  await expect(page.locator('#status')).not.toContainText('Saved and rebuilt');
  await expect.poll(async () => page.locator('#draft-indicator').textContent()).toContain('Draft saved locally');
  expect(await fs.readFile(path.join(vault, 'article.md'), 'utf8')).toContain('Original');
  const storedDraft = await page.evaluate(() => new Promise(resolve => { const request = indexedDB.open('oxpio-editor', 1); request.onsuccess = () => { const get = request.result.transaction('drafts', 'readonly').objectStore('drafts').getAll(); get.onsuccess = () => resolve(get.result); }; request.onerror = () => resolve([]); }));
  expect(storedDraft.length).toBeGreaterThan(0);
  page.on('dialog', async dialog => { await dialog.accept(); });
  await page.reload();
  await expect(page.locator('#status')).toContainText('Local draft restored');
  await expect(page.locator('#editor .cm-content')).toContainText('Local candidate');
  await context.close();
});

test('diff marks deleted candidate content', async ({browser}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_oxpio/editor?path=article.md`);
  await expect(page.locator('#editor .cm-content')).toBeVisible();
  await page.locator('#source-mode').click();
  await page.locator('#editor .cm-content').fill('---\ntitle: Article\npublish: true\ntype: doc\n---\n');
  await page.locator('#diff-mode').click();
  await expect(page.locator('#diff-editor .merge-host')).toBeVisible();
  await expect(page.locator('#diff-editor .cm-deletedChunk, #diff-editor .cm-deletedLine')).not.toHaveCount(0);
  await context.close();
});

test('visual local drafts restore metadata changes before save', async ({browser}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_oxpio/editor?path=article.md`);
  await page.locator('[data-field="title"]').fill('Visual local title');
  await expect.poll(async () => page.locator('#draft-indicator').textContent()).toContain('Draft saved locally');
  page.on('dialog', async dialog => { await dialog.accept(); });
  await page.reload();
  await expect(page.locator('#status')).toContainText('Local draft restored');
  await expect(page.locator('#editor-footer #status')).toBeVisible();
  await expect(page.locator('#save-state')).toHaveText('Unsaved');
  await expect(page.locator('[data-field="title"]')).toHaveValue('Visual local title');
  await page.getByRole('button', {name: 'Save'}).click();
  await expect(page.locator('#status')).toContainText('Saved and rebuilt');
  expect(await fs.readFile(path.join(vault, 'article.md'), 'utf8')).toContain('title: Visual local title');
  await context.close();
});

test('editor setup initializes one account and failed setup does not write', async () => {
  test.skip(process.platform !== 'linux', 'the interactive setup check uses a Linux pseudo-terminal');
  try {
    execFileSync('script', ['--version'], {stdio: 'ignore'});
  } catch {
    test.skip(true, 'script is required for the interactive setup check');
  }
  const setupVault = path.join(tempRoot, 'setup-vault');
  await fs.mkdir(setupVault);
  const config = 'title: Setup\nbaseURL: https://example.test/\nnavigation: []\n';
  await fs.writeFile(path.join(setupVault, 'oxpio.yaml'), config);
  await fs.writeFile(path.join(setupVault, '_index.md'), '---\ntitle: Home\npublish: true\n---\nHome\n');
  const setupOutput = execFileSync('script', ['-qef', '--echo', 'never', '-c', `${shellQuote(binaryPath)} editor --setup --vault ${shellQuote(setupVault)}`, '/dev/null'], {input: 'admin\nsecret\n', encoding: 'utf8'});
  expect(setupOutput).not.toContain('secret');
  const configured = await fs.readFile(path.join(setupVault, 'oxpio.yaml'), 'utf8');
  expect(configured).toContain('username: "admin"');
  expect(configured).toContain('passwordHash: $argon2id$');

  const before = configured;
  let failed = false;
  try {
    execFileSync(binaryPath, ['editor', '--setup', '--vault', setupVault], {input: 'other\nother\n', encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe']});
  } catch (error) {
    failed = true;
    expect(`${error.stdout || ''}${error.stderr || ''}`).toContain('already configured');
  }
  expect(failed).toBe(true);
  expect(await fs.readFile(path.join(setupVault, 'oxpio.yaml'), 'utf8')).toBe(before);
});

test('editor startup fails before listening when the account is missing', async () => {
  const missingVault = path.join(tempRoot, 'missing-account');
  await fs.mkdir(missingVault);
  await fs.writeFile(path.join(missingVault, 'oxpio.yaml'), 'title: Missing\nbaseURL: https://example.test/\nnavigation: []\n');
  await fs.writeFile(path.join(missingVault, '_index.md'), '---\ntitle: Home\npublish: true\n---\nHome\n');
  const port = await freePort();
  const failed = spawn(binaryPath, ['editor', '--vault', missingVault, '--port', String(port)], {cwd: repoRoot, stdio: ['ignore', 'pipe', 'pipe']});
  let stdout = '';
  let stderr = '';
  failed.stdout.on('data', data => { stdout += data; });
  failed.stderr.on('data', data => { stderr += data; });
  await waitForExit(failed);
  expect(failed.exitCode).not.toBe(0);
  expect(`${stdout}${stderr}`).toContain('editor.username and editor.passwordHash');
  await expect.poll(async () => {
    try { return (await fetch(`http://127.0.0.1:${port}/`)).status; } catch { return 0; }
  }).toBe(0);
});
