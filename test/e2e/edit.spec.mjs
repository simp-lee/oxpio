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
  await page.goto(`${origin}/_obsite/login`);
  await page.locator('input[name="username"]').fill('admin');
  await page.locator('input[name="password"]').fill('secret');
  await page.getByRole('button', {name: 'Log in'}).click();
  await expect(page.locator('body')).toContainText('authenticated');
}

async function session(context) {
  const response = await context.request.get(`${origin}/_obsite/session`);
  expect(response.status()).toBe(200);
  return response.json();
}

async function source(context, relPath) {
  const response = await context.request.get(`${origin}/_obsite/source?path=${encodeURIComponent(relPath)}`);
  return {
    response,
    body: await response.text(),
    hash: response.headers()['x-obsite-source-hash'] || ''
  };
}

async function mutate(context, method, url, csrf, hash, data, contentType = '') {
  const headers = {
    Origin: origin,
    'X-Obsite-CSRF': csrf,
    'X-Obsite-Source-Hash': hash
  };
  if (contentType) headers['Content-Type'] = contentType;
  return context.request.fetch(`${origin}${url}`, {method, headers, data});
}

async function startConfiguredEditor() {
  vault = path.join(tempRoot, 'vault');
  await fs.mkdir(vault);
  await fs.writeFile(path.join(vault, 'obsite.yaml'), `title: Edit E2E\nbaseURL: http://127.0.0.1/\nnavigation: []\nedit:\n  username: admin\n  passwordHash: ${passwordHash}\n`);
  await fs.writeFile(path.join(vault, '_index.md'), '---\ntitle: Home\npublish: true\n---\nHome\n');
  await fs.writeFile(path.join(vault, 'guide.md'), '---\ntitle: Guide\npublish: true\ntype: doc\n---\nGuide\n');
  await fs.mkdir(path.join(vault, 'section'));
  await fs.writeFile(path.join(vault, 'section', '_index.md'), '---\ntitle: Section\npublish: true\n---\nSection\n');
  await fs.writeFile(path.join(vault, 'article.md'), '---\n# preserve this comment\ntitle: "Article"\npublish: true\ntype: doc\n---\n\nOriginal\n');
  await fs.writeFile(path.join(vault, 'draft.md'), '---\ntitle: Draft\npublish: false\ntype: doc\n---\nPrivate\n');
  const port = await freePort();
  origin = `http://127.0.0.1:${port}`;
  const configPath = path.join(vault, 'obsite.yaml');
  const config = (await fs.readFile(configPath, 'utf8')).replace('http://127.0.0.1/', `${origin}/`);
  await fs.writeFile(configPath, config);
  child = spawn(binaryPath, ['edit', '--vault', vault, '--port', String(port)], {cwd: repoRoot, stdio: 'ignore'});
  await waitForHTTP(`${origin}/_obsite/login`);
}

test.beforeEach(async () => {
  tempRoot = await fs.mkdtemp(path.join(os.tmpdir(), 'obsite-edit-e2e-'));
  binaryPath = path.join(tempRoot, process.platform === 'win32' ? 'obsite.exe' : 'obsite');
  execFileSync('go', ['build', '-o', binaryPath, './cmd/obsite'], {cwd: repoRoot});
  await startConfiguredEditor();
});

test.afterEach(async () => {
  if (child && !child.killed) child.kill('SIGTERM');
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
  const sourceResponse = await context.request.get(`${origin}/_obsite/source?path=article.md`);
  expect(sourceResponse.status()).toBe(401);
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

  await page.goto(`${origin}/article/`);
  await expect(page.locator('.edit-page-link')).toHaveAttribute('href', /path=article\.md/);
  await page.goto(`${origin}/section/`);
  await expect(page.locator('.edit-page-link')).toHaveAttribute('href', /path=section%2F_index\.md/);
  await page.goto(`${origin}/_obsite/editor?path=article.md`);
  await expect(page.locator('#editor .cm-content')).toContainText('Original');
  await page.locator('#mode').click();
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

test('editor visual states hide irrelevant controls and protect frontmatter during media insertion', async ({browser}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_obsite/editor?path=section/_index.md`);
  await expect(page.locator('#editor .cm-content')).toBeVisible();
  await expect(page.locator('.metadata-advanced')).toBeHidden();
  await page.goto(`${origin}/_obsite/editor?path=article.md`);
  const originalSource = (await source(context, 'article.md')).body;
  const closingDelimiter = originalSource.indexOf('\n---\n');
  const originalFrontmatter = originalSource.slice(0, closingDelimiter + '\n---\n'.length);

  await expect(page.locator('#preview-empty')).toBeVisible();
  await page.locator('#preview').click();
  await expect(page.locator('#preview-frame')).toBeVisible();
  await expect(page.locator('#preview-empty')).toBeHidden();

  await page.locator('#file-new-folder').click();
  await expect(page.locator('#markdown-file-fields')).toBeHidden();
  await page.locator('#file-dialog').evaluate(dialog => dialog.close());
  await page.locator('#file-rename').click();
  await expect(page.locator('#markdown-file-fields')).toBeHidden();
  await page.locator('#file-dialog').evaluate(dialog => dialog.close());
  await page.locator('#new').click();
  await expect(page.locator('#new-dialog')).toBeVisible();
  await page.locator('#new-dialog').evaluate(dialog => dialog.close());

  await page.locator('#mode').click();
  await page.locator('#media').click();
  await page.locator('#media-upload').setInputFiles(path.join(repoRoot, 'test', 'testdata', 'e2e', 'runtime-vault', 'images', 'hero.png'));
  await page.waitForFunction(() => document.querySelectorAll('#media-list .media-item').length > 0);
  await expect.poll(async () => (await page.locator('#editor .cm-line').allTextContents()).join('\n')).toMatch(/^---[\s\S]*---\r?\n\r?\n!\[/);
  const editorSource = (await page.locator('#editor .cm-line').allTextContents()).join('\n');
  expect(editorSource.startsWith(`${originalFrontmatter}\n![hero](uploads/hero.png)`)).toBe(true);
  await page.getByRole('button', {name: 'Save'}).click();
  await expect(page.locator('#status')).toContainText('Saved and rebuilt');
  const persisted = await source(context, 'article.md');
  expect(persisted.body.startsWith(`${originalFrontmatter}\n![hero](uploads/hero.png)`)).toBe(true);
  await page.reload();
  await page.locator('#editor .cm-content').waitFor();
  await page.locator('#mode').click();
  await expect.poll(async () => (await page.locator('#editor .cm-line').allTextContents()).join('\n')).toContain('![hero](uploads/hero.png)');
  await context.close();
});

test('source media insertion preserves mixed line endings after save and reload', async ({browser}) => {
  const mixedSource = '---\r\n# preserve this comment\r\ntitle: "Article"\r\npublish: true\r\ntype: doc\r\n---\r\n\nOriginal\n';
  await fs.writeFile(path.join(vault, 'article.md'), mixedSource);
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_obsite/editor?path=article.md`);
  const delimiter = mixedSource.indexOf('\r\n---\r\n') + '\r\n---\r\n'.length;
  const originalFrontmatter = mixedSource.slice(0, delimiter);
  const expected = `${originalFrontmatter}\r\n![hero](uploads/hero.png)\r\n\nOriginal\n`;
  await page.locator('#mode').click();
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
  await page.goto(`${origin}/_obsite/editor?path=article.md`);
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
  await page.goto(`${origin}/_obsite/editor?path=article.md`);
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
  await context.close();
});

test('editor mobile layout keeps the workspace ahead of navigation', async ({browser}) => {
  const context = await browser.newContext({viewport: {width: 390, height: 844}});
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_obsite/editor?path=article.md`);
  await page.locator('#editor .cm-content').waitFor();
  await expect(page.locator('.metadata')).toBeVisible();
  await expect(page.locator('.toolbar')).toBeVisible();
  const layout = await page.evaluate(() => {
    const workspace = document.querySelector('.workspace').getBoundingClientRect();
    const sidebar = document.querySelector('.source-sidebar').getBoundingClientRect();
    const editor = document.querySelector('#editor').getBoundingClientRect();
    return {workspaceTop: workspace.top, sidebarTop: sidebar.top, editorWidth: editor.width, viewportWidth: innerWidth};
  });
  expect(layout.workspaceTop).toBeLessThan(layout.sidebarTop);
  expect(layout.editorWidth).toBeLessThanOrEqual(layout.viewportWidth);
  await context.close();
});

test('browser create defaults to a private draft, rejects bad posts, and deletes after confirmation', async ({browser}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_obsite/editor?path=article.md`);

  await page.getByRole('button', {name: 'New article'}).click();
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

  await page.goto(`${origin}/_obsite/editor?path=new-browser.md`);
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('button', {name: 'Delete article'}).click();
  await expect.poll(async () => (await fs.access(path.join(vault, 'new-browser.md')).then(() => true).catch(() => false))).toBe(false);
  expect((await context.request.get(`${origin}/new-browser/`)).status()).toBe(404);

  const csrf = (await session(context)).csrf;
  const badPost = await mutate(context, 'POST', '/_obsite/source', csrf, 'absent', new URLSearchParams({path: 'missing-date.md', title: 'Bad post', type: 'post'}).toString(), 'application/x-www-form-urlencoded');
  expect(badPost.status()).toBe(400);
  expect(await badPost.text()).toContain('date is required');
  await context.close();
});

test('failed builds and stale hashes are visible and leave source and output unchanged', async ({browser}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page);
  const before = await source(context, 'article.md');
  const beforePage = await context.request.get(`${origin}/article/`);
  const beforeHTML = await beforePage.text();

  await page.goto(`${origin}/_obsite/editor?path=article.md`);
  await page.locator('#mode').click();
  await page.locator('#editor .cm-content').fill('---\ntitle: Broken\npublish: true\ntype: invalid\n---\nFailure\n');
  await page.getByRole('button', {name: 'Save'}).click();
  await expect(page.locator('#status')).toContainText('Save failed');
  const afterFailure = await source(context, 'article.md');
  expect(afterFailure.body).toBe(before.body);
  expect((await (await context.request.get(`${origin}/article/`)).text())).toBe(beforeHTML);

  const external = '---\ntitle: External\npublish: true\ntype: doc\n---\nExternal\n';
  await fs.writeFile(path.join(vault, 'article.md'), external);
  const stale = await mutate(context, 'PUT', '/_obsite/source?path=article.md', (await session(context)).csrf, before.hash, 'stale');
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
  await editorPage.goto(`${origin}/_obsite/editor?path=article.md`);
  await editorPage.locator('#mode').click();
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
    const control = await fetch(`${serveOrigin}/_obsite/login`);
    expect(control.status).toBe(404);
    const sourceResponse = await fetch(`${serveOrigin}/_obsite/source?path=article.md`);
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
  const page = await context.newPage();
  await login(page);
  await page.goto(`${origin}/_obsite/editor?path=draft.md`);
  await expect(page.locator('[data-field="title"]')).toHaveValue('Draft');
  await expect(page.locator('#editor .cm-content')).toContainText('Private');
  await expect(page.locator('#editor .cm-content')).not.toContainText('title: Draft');

  await page.getByRole('button', {name: 'Preview draft'}).click();
  await expect(page.locator('#preview-frame')).not.toBeHidden();
  await expect(page.frameLocator('#preview-frame').locator('body')).toContainText('Private');

  await page.locator('[data-field="title"]').fill('Updated draft');
  await page.locator('[data-field="description"]').fill('A draft description');
  await page.getByRole('button', {name: 'Save'}).click();
  await expect(page.locator('#status')).toContainText('Saved and rebuilt');
  const saved = await fs.readFile(path.join(vault, 'draft.md'), 'utf8');
  expect(saved).toContain('title: Updated draft');
  expect(saved).toContain('description: A draft description');
  expect(saved).toContain('publish: false');

  await page.getByRole('button', {name: 'Media library'}).click();
  const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=', 'base64');
  await page.locator('#media-upload').setInputFiles({name: 'editor-pixel.png', mimeType: 'image/png', buffer: png});
  await expect(page.locator('.media-item')).toContainText('editor-pixel.png');
  await expect(page.locator('#status')).toContainText('Uploaded uploads/editor-pixel.png');
  await page.getByRole('button', {name: 'Save'}).click();
  await expect(page.locator('#status')).toContainText('Saved and rebuilt');
  const mediaSaved = await fs.readFile(path.join(vault, 'draft.md'), 'utf8');
  expect(mediaSaved).toContain('editor-pixel.png');

  await page.getByRole('button', {name: 'New folder'}).click();
  await page.locator('#file-path').fill('docs');
  await page.locator('#file-submit').click();
  await expect(page.locator('.file-entry[data-path="docs"]')).toHaveCount(1);

  await page.getByRole('button', {name: 'New Markdown'}).click();
  await page.locator('#file-path').fill('docs/_index.md');
  await page.locator('#file-title').fill('Docs');
  await page.locator('#file-kind').selectOption('section');
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
  page.once('dialog', dialog => dialog.accept());
  await page.locator('#file-delete').click();
  await expect(page.locator('.file-entry[data-path="docs"]')).toHaveCount(0);
  await context.close();
});

test('edit setup initializes one account and failed setup does not write', async () => {
  test.skip(process.platform !== 'linux', 'the interactive setup check uses a Linux pseudo-terminal');
  try {
    execFileSync('script', ['--version'], {stdio: 'ignore'});
  } catch {
    test.skip(true, 'script is required for the interactive setup check');
  }
  const setupVault = path.join(tempRoot, 'setup-vault');
  await fs.mkdir(setupVault);
  const config = 'title: Setup\nbaseURL: https://example.test/\nnavigation: []\n';
  await fs.writeFile(path.join(setupVault, 'obsite.yaml'), config);
  await fs.writeFile(path.join(setupVault, '_index.md'), '---\ntitle: Home\npublish: true\n---\nHome\n');
  const setupOutput = execFileSync('script', ['-qef', '--echo', 'never', '-c', `${shellQuote(binaryPath)} edit --setup --vault ${shellQuote(setupVault)}`, '/dev/null'], {input: 'admin\nsecret\n', encoding: 'utf8'});
  expect(setupOutput).not.toContain('secret');
  const configured = await fs.readFile(path.join(setupVault, 'obsite.yaml'), 'utf8');
  expect(configured).toContain('username: "admin"');
  expect(configured).toContain('passwordHash: $argon2id$');

  const before = configured;
  let failed = false;
  try {
    execFileSync(binaryPath, ['edit', '--setup', '--vault', setupVault], {input: 'other\nother\n', encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe']});
  } catch (error) {
    failed = true;
    expect(`${error.stdout || ''}${error.stderr || ''}`).toContain('already configured');
  }
  expect(failed).toBe(true);
  expect(await fs.readFile(path.join(setupVault, 'obsite.yaml'), 'utf8')).toBe(before);
});

test('edit startup fails before listening when the account is missing', async () => {
  const missingVault = path.join(tempRoot, 'missing-account');
  await fs.mkdir(missingVault);
  await fs.writeFile(path.join(missingVault, 'obsite.yaml'), 'title: Missing\nbaseURL: https://example.test/\nnavigation: []\n');
  await fs.writeFile(path.join(missingVault, '_index.md'), '---\ntitle: Home\npublish: true\n---\nHome\n');
  const port = await freePort();
  const failed = spawn(binaryPath, ['edit', '--vault', missingVault, '--port', String(port)], {cwd: repoRoot, stdio: ['ignore', 'pipe', 'pipe']});
  let stdout = '';
  let stderr = '';
  failed.stdout.on('data', data => { stdout += data; });
  failed.stderr.on('data', data => { stderr += data; });
  await waitForExit(failed);
  expect(failed.exitCode).not.toBe(0);
  expect(`${stdout}${stderr}`).toContain('edit.username and edit.passwordHash');
  await expect.poll(async () => {
    try { return (await fetch(`http://127.0.0.1:${port}/`)).status; } catch { return 0; }
  }).toBe(0);
});
