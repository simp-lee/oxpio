import {chromium} from '@playwright/test';
import {spawn, execFileSync} from 'node:child_process';
import {createServer} from 'node:http';
import {promises as fs} from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const repoRoot = path.resolve(import.meta.dirname, '..');
const outputDir = path.join(repoRoot, '.pi-work', 'ui-design');
const runtimeFixture = path.join(repoRoot, 'test', 'testdata', 'e2e', 'runtime-vault');
const featureFixture = path.join(repoRoot, 'test', 'testdata', 'e2e', 'feature-vault');
const passwordHash = '$argon2id$v=19$m=19456,t=2,p=1$o2KEDd/Nrt/G5QtUKhAY3w$9gLt3Rz/TkUAAJl6HSPSjjC1xbg3rAKuEq0AJQzyp5M';
const tempRoot = await fs.mkdtemp(path.join(os.tmpdir(), 'obsite-ui-design-'));
const binaryPath = path.join(tempRoot, process.platform === 'win32' ? 'obsite.exe' : 'obsite');
const roots = new Map();
const manifest = [];
let staticOrigin;
let editorOrigin;
let staticServer;
let editorProcess;

function contentType(filePath) {
  switch (path.extname(filePath).toLowerCase()) {
    case '.html': return 'text/html; charset=utf-8';
    case '.js': return 'text/javascript; charset=utf-8';
    case '.css': return 'text/css; charset=utf-8';
    case '.json': return 'application/json; charset=utf-8';
    case '.xml': return 'application/xml; charset=utf-8';
    case '.png': return 'image/png';
    case '.svg': return 'image/svg+xml';
    case '.woff2': return 'font/woff2';
    case '.woff': return 'font/woff';
    case '.ttf': return 'font/ttf';
    default: return 'application/octet-stream';
  }
}

async function listen(server) {
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  return server.address().port;
}

async function freePort() {
  const server = createServer();
  const port = await listen(server);
  await new Promise(resolve => server.close(resolve));
  return port;
}

async function waitForHTTP(url, timeout = 20_000) {
  const deadline = Date.now() + timeout;
  let lastError;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url);
      if (response.status < 500) return response;
      lastError = new Error(`HTTP ${response.status}`);
    } catch (error) {
      lastError = error;
    }
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw lastError || new Error(`timed out waiting for ${url}`);
}

async function serveStatic(req, res) {
  const requestURL = new URL(req.url, staticOrigin || 'http://127.0.0.1');
  const match = /^\/(alpha|feature)(\/.*)?$/.exec(requestURL.pathname);
  if (!match) return res.writeHead(404).end('not found');
  const root = roots.get(match[1]);
  let relative;
  try {
    relative = decodeURIComponent(match[2] || '').replace(/^\/+/, '');
  } catch {
    return res.writeHead(400).end('bad path');
  }
  if (!relative || relative.endsWith('/')) relative += 'index.html';
  const candidate = path.resolve(root, relative);
  if (candidate !== root && !candidate.startsWith(root + path.sep)) return res.writeHead(403).end('forbidden');
  let filePath = candidate;
  let status = 200;
  try {
    await fs.access(filePath);
  } catch {
    filePath = path.join(root, '404.html');
    status = 404;
  }
  try {
    const data = await fs.readFile(filePath);
    res.writeHead(status, {'content-type': contentType(filePath)}).end(data);
  } catch {
    res.writeHead(404).end('not found');
  }
}

async function copyAndBuild(name, fixture, basePath, baseURL) {
  const vault = path.join(tempRoot, name);
  await fs.cp(fixture, vault, {recursive: true});
  const configPath = path.join(vault, 'obsite.yaml');
  let config = await fs.readFile(configPath, 'utf8');
  if (name === 'feature') {
    config = `title: Quiet Notes\nbaseURL: https://example.com/blog/\nauthor: Obsite team\ndescription: Thoughtful documentation for small, durable tools.\nnavigation:\n  - name: Home\n    section: .\n  - name: Guide\n    section: guide\n  - name: Journal\n    url: /updates/\nsidebar:\n  enabled: true\npopover:\n  enabled: true\nrelated:\n  enabled: true\n  count: 2\nrss:\n  enabled: true\ntimeline:\n  enabled: true\n  path: updates\nsource:\n  editURL: https://example.com/edit/:path\n  viewURL: https://example.com/view/:path\n`;
    await fs.writeFile(path.join(vault, '_index.md'), `---\ntitle: Quiet Notes\npublish: true\ndescription: A focused space for clear ideas, durable documentation, and calm publishing.\n---\n\nA small, readable site for people who care about the words as much as the tooling.\n`);
    await fs.writeFile(path.join(vault, 'guide', '_index.md'), `---\ntitle: Guide\npublish: true\ndescription: Practical patterns for building and maintaining a useful site.\n---\n\nStart with the structure, then make every page easy to return to.\n`);
    await fs.writeFile(path.join(vault, 'guide', 'article.md'), `---\ntitle: Build a calm publishing workflow\npublish: true\ntype: doc\ndate: 2026-04-06\nupdated: 2026-04-08\nauthor: Alice Example\ndescription: A practical approach to turning notes into a site people enjoy reading.\nstatus: stable\naudience: Builders\nproductVersion: "2.0"\nseries: Quiet Notes\ntags:\n  - writing\n  - workflow\n---\n# Build a calm publishing workflow\n\nGood publishing starts with a clear source and ends with a page that gets out of the reader's way.\n\n## Start with a useful structure\n\nKeep navigation predictable and let each page do one job well.\n\n> [!note] A useful rule\n> Prefer a small number of clear choices over a dashboard full of controls.\n\n## Review before you publish\n\nA fast preview loop makes the final result feel intentional.\n`);
    await fs.writeFile(path.join(vault, 'guide', 'field-notes.md'), `---\ntitle: Field notes on readable docs\npublish: true\ntype: post\ndate: 2026-04-02\nauthor: Obsite team\ndescription: Small editorial decisions that make technical writing easier to use.\ntags:\n  - writing\n---\n\nReadable documentation is a product feature.\n`);
    await fs.writeFile(path.join(vault, 'guide', 'quick-start.md'), `---\ntitle: A quick start for thoughtful sites\npublish: true\ntype: doc\ndate: 2026-03-28\nauthor: Obsite team\ndescription: The shortest path from a folder of notes to a welcoming site.\ntags:\n  - workflow\n---\n\nChoose a clear title, write one useful paragraph, and publish when the page feels ready.\n`);
    await fs.writeFile(path.join(vault, 'guide', 'ship-notes.md'), `---\ntitle: Ship notes, not noise\npublish: true\ntype: post\ndate: 2026-03-25\nauthor: Obsite team\ndescription: A few principles for keeping a publishing rhythm sustainable.\ntags:\n  - writing\n---\n\nA sustainable rhythm is easier to keep when every note earns its place.\n`);
    await fs.writeFile(path.join(vault, 'roadmap.md'), `---\ntitle: Roadmap\npublish: true\ntype: page\n---\n\nA short view of what is coming next.\n`);
  }
  await fs.writeFile(configPath, config.replace(baseURL, `${staticOrigin}${basePath}`));
  execFileSync(binaryPath, ['build', '--vault', vault], {cwd: repoRoot, stdio: 'inherit'});
  roots.set(name, path.join(vault, 'public'));
}

async function prepareEditor() {
  const vault = path.join(tempRoot, 'editor-vault');
  await fs.mkdir(path.join(vault, 'section', 'nested'), {recursive: true});
  await fs.writeFile(path.join(vault, 'obsite.yaml'), `title: Obsite Studio\nbaseURL: http://127.0.0.1/\nauthor: Studio Team\ndescription: A calm writing workspace.\nnavigation:\n  - name: Home\n    section: .\n  - name: Notes\n    section: section\nsidebar:\n  enabled: true\nedit:\n  username: admin\n  passwordHash: ${passwordHash}\n`);
  await fs.writeFile(path.join(vault, '_index.md'), '---\ntitle: Studio Home\npublish: true\n---\n# Studio Home\n\nWelcome to the editorial workspace.\n');
  await fs.mkdir(path.join(vault, 'images'), {recursive: true});
  await fs.copyFile(path.join(runtimeFixture, 'images', 'hero.png'), path.join(vault, 'images', 'hero.png'));
  await fs.writeFile(path.join(vault, 'article.md'), `---\ntitle: Reliable publishing in one small workflow\npublish: true\ntype: doc\ndate: 2026-04-06\ndescription: A compact article showing formulas, code, tables, callouts, and a local image.\nauthor: Obsite team\nupdated: 2026-04-08\ntags:\n  - writing\n  - markdown\nstatus: stable\nslug: reliable-publishing\n---\n> [!info] Editor showcase\n> This page is rendered from a local Markdown source and can be edited without leaving the vault.\n\nGood publishing keeps the source readable and the final page predictable.\n\n![A calm workspace](images/hero.png)\n\n## A small formula\n\nInline math stays close to the sentence: $E = mc^2$.\n\n$$\n\\operatorname{score}(x) = \\frac{\\sum_i w_i x_i}{\\sum_i w_i}\n$$\n\n## A practical code sample\n\n\`\`\`go\nfunc Publish(source []byte) error {\n    return validateAndBuild(source)\n}\n\`\`\`\n\n## A compact comparison\n\n| Stage | Input | Result |\n| --- | --- | --- |\n| Edit | Markdown source | Clear intent |\n| Preview | Candidate bytes | Rendered draft |\n| Publish | Validated build | Atomic output |\n\n## Keep the loop short\n\nChange one thing, preview the result, and publish only when the page is ready.\n`);
  await fs.writeFile(path.join(vault, 'draft.md'), '---\ntitle: Private draft\npublish: false\ntype: doc\n---\nWork in progress.\n');
  await fs.writeFile(path.join(vault, 'section', '_index.md'), '---\ntitle: Notes\npublish: true\n---\nA collection of notes.\n');
  await fs.writeFile(path.join(vault, 'section', 'nested', '_index.md'), '---\ntitle: Nested notes\npublish: true\n---\nNested material.\n');
  await fs.writeFile(path.join(vault, 'section', 'nested', 'note.md'), '---\ntitle: A nested note\npublish: true\ntype: doc\n---\nNested note content.\n');

  const port = await freePort();
  editorOrigin = `http://127.0.0.1:${port}`;
  const configPath = path.join(vault, 'obsite.yaml');
  const config = (await fs.readFile(configPath, 'utf8')).replace('http://127.0.0.1/', `${editorOrigin}/`);
  await fs.writeFile(configPath, config);
  editorProcess = spawn(binaryPath, ['edit', '--vault', vault, '--port', String(port)], {cwd: repoRoot, stdio: 'ignore'});
  await waitForHTTP(`${editorOrigin}/_obsite/login`);
}

async function waitForEditorExit() {
  if (!editorProcess || editorProcess.exitCode !== null) return;
  await new Promise(resolve => editorProcess.once('exit', resolve));
}

async function settle(page) {
  await page.waitForLoadState('domcontentloaded').catch(() => {});
  await page.evaluate(() => document.fonts?.ready).catch(() => {});
  await page.waitForTimeout(350);
}

async function capture(page, file, description, options = {}) {
  await settle(page);
  await page.screenshot({path: path.join(outputDir, file), fullPage: options.fullPage ?? true});
  manifest.push({file, description, viewport: await page.evaluate(() => `${innerWidth}×${innerHeight}`)});
}

async function publicPage(page, route, file, description, options = {}) {
  await page.goto(`${staticOrigin}${route}`, {waitUntil: 'domcontentloaded'});
  await capture(page, file, description, options);
}

async function capturePublic(browser) {
  const context = await browser.newContext({viewport: {width: 1440, height: 900}, colorScheme: 'light'});
  const page = await context.newPage();
  await publicPage(page, '/feature/', 'public-home.png', 'Feature site home page with masthead, navigation, banner, and custom CSS.');
  await publicPage(page, '/feature/guide/', 'public-section.png', 'Section landing page with sidebar and article collection.');
  await publicPage(page, '/feature/guide/article/', 'public-article.png', 'Article page with metadata, callout, source links, and reading flow.');
  await publicPage(page, '/feature/updates/', 'public-timeline.png', 'Timeline page.');
  await publicPage(page, '/feature/tags/writing/', 'public-tag.png', 'Tag archive page for writing notes.');
  await publicPage(page, '/feature/missing/', 'public-404.png', 'Not-found page.', {fullPage: false});
  await publicPage(page, '/alpha/math-diagrams/', 'public-math-diagrams.png', 'Math and Mermaid rendering page.');
  await publicPage(page, '/alpha/child/child/', 'public-rich-article.png', 'Article with banner, code block, and wide table.');
  const popoverLink = page.locator('[data-page-content] a[data-popover-path="reference.md"]');
  if (await popoverLink.count()) {
    await popoverLink.focus();
    await page.waitForFunction(() => document.querySelector('[data-popover-card]')?.getAttribute('aria-hidden') === 'false');
    await page.locator('[data-popover-card]').waitFor({state: 'visible'});
    await capture(page, 'public-popover.png', 'Internal-link popover preview.');
  }
  await page.goto(`${staticOrigin}/feature/guide/article/`);
  await page.locator('[data-theme-toggle]').click();
  await page.waitForTimeout(200);
  await capture(page, 'public-article-dark.png', 'Article page in explicit dark mode.');
  await context.close();

  const mobile = await browser.newContext({viewport: {width: 390, height: 844}, colorScheme: 'light'});
  const mobilePage = await mobile.newPage();
  await publicPage(mobilePage, '/alpha/child/child/', 'public-mobile-article.png', 'Mobile article layout with locally scrolling content.', {fullPage: false});
  await mobilePage.locator('[data-sidebar-toggle]').click();
  await mobilePage.waitForTimeout(150);
  await capture(mobilePage, 'public-mobile-sidebar.png', 'Mobile navigation drawer open.', {fullPage: false});
  await mobile.close();
}

async function login(page) {
  await page.goto(`${editorOrigin}/_obsite/login`, {waitUntil: 'domcontentloaded'});
  await page.locator('input[name="username"]').fill('admin');
  await page.locator('input[name="password"]').fill('secret');
  await page.getByRole('button', {name: 'Log in'}).click();
  await page.waitForTimeout(200);
}

async function captureEditor(browser) {
  const context = await browser.newContext({viewport: {width: 1440, height: 900}, colorScheme: 'light'});
  const page = await context.newPage();
  await page.goto(`${editorOrigin}/_obsite/login`, {waitUntil: 'domcontentloaded'});
  await capture(page, 'editor-login.png', 'Editor login screen.');
  await login(page);
  await page.goto(`${editorOrigin}/_obsite/editor?path=article.md`, {waitUntil: 'domcontentloaded'});
  await page.locator('#editor .cm-content').waitFor();
  await capture(page, 'editor-visual.png', 'Authenticated visual editor with metadata, toolbar, source list, and file manager.');
  await page.locator('#new').click();
  await page.locator('#new-dialog').waitFor({state: 'visible'});
  await capture(page, 'editor-new-article.png', 'New article draft dialog.');
  await page.locator('#new-dialog').evaluate(dialog => dialog.close());

  await page.locator('#preview').click();
  await page.locator('#preview-frame').waitFor({state: 'visible'});
  await page.locator('#preview-empty').waitFor({state: 'hidden'});
  await capture(page, 'editor-preview.png', 'Content-only server-rendered draft preview beside the editor.');
  await page.locator('#mode').click();
  await page.locator('#mode').filter({hasText: 'Form mode'}).waitFor();
  await capture(page, 'editor-source.png', 'Full Markdown/YAML source editing mode.');
  await page.locator('#mode').click();
  await page.locator('#mode').filter({hasText: 'Source mode'}).waitFor();

  await page.locator('#media').click();
  await page.locator('#media-panel').waitFor({state: 'visible'});
  await page.locator('#media-upload').setInputFiles(path.join(runtimeFixture, 'images', 'hero.png'));
  await page.waitForFunction(() => document.querySelectorAll('#media-list .media-item').length > 0);
  await capture(page, 'editor-media-library.png', 'Media library with uploaded image and insertion controls.');
  await page.locator('#media-close').click();

  await page.goto(`${editorOrigin}/_obsite/editor?path=article.md`, {waitUntil: 'domcontentloaded'});
  await page.locator('#editor .cm-content').waitFor();
  await page.locator('#file-new-markdown').click();
  await page.locator('#file-dialog').waitFor({state: 'visible'});
  await capture(page, 'editor-new-markdown.png', 'File manager New Markdown dialog.');
  await page.locator('#file-dialog').evaluate(dialog => dialog.close());
  await page.locator('#file-new-folder').click();
  await page.locator('#file-dialog').waitFor({state: 'visible'});
  await capture(page, 'editor-new-folder.png', 'File manager New folder dialog.');
  await page.locator('#file-dialog').evaluate(dialog => dialog.close());
  await page.locator('#file-rename').click();
  await page.locator('#file-dialog').waitFor({state: 'visible'});
  await capture(page, 'editor-rename.png', 'File manager Rename dialog with CAS-protected selected path.');
  await page.locator('#file-dialog').evaluate(dialog => dialog.close());
  const storageState = await context.storageState();
  const mobile = await browser.newContext({storageState, viewport: {width: 390, height: 844}, colorScheme: 'light'});
  const mobilePage = await mobile.newPage();
  await mobilePage.goto(`${editorOrigin}/_obsite/editor?path=article.md`, {waitUntil: 'domcontentloaded'});
  await mobilePage.locator('#editor .cm-content').waitFor();
  await capture(mobilePage, 'editor-mobile.png', 'Responsive editor layout on a narrow viewport.', {fullPage: false});
  await mobile.close();

  const dark = await browser.newContext({storageState, viewport: {width: 1440, height: 900}, colorScheme: 'dark'});
  const darkPage = await dark.newPage();
  await darkPage.goto(`${editorOrigin}/_obsite/editor?path=article.md`, {waitUntil: 'domcontentloaded'});
  await darkPage.locator('#editor .cm-content').waitFor();
  await capture(darkPage, 'editor-dark.png', 'Editor in system dark mode.', {fullPage: false});
  await dark.close();
  await context.close();
}

let browser;
try {
  await fs.rm(outputDir, {recursive: true, force: true});
  await fs.mkdir(outputDir, {recursive: true});
  execFileSync('go', ['build', '-o', binaryPath, './cmd/obsite'], {cwd: repoRoot, stdio: 'inherit'});
  staticServer = createServer((req, res) => void serveStatic(req, res));
  const staticPort = await listen(staticServer);
  staticOrigin = `http://127.0.0.1:${staticPort}`;
  await copyAndBuild('alpha', runtimeFixture, '/alpha/', 'http://127.0.0.1/alpha/');
  await copyAndBuild('feature', featureFixture, '/feature/', 'https://example.com/blog/');
  await prepareEditor();

  browser = await chromium.launch({headless: true});
  await capturePublic(browser);
  await captureEditor(browser);
  await fs.writeFile(path.join(outputDir, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n');
  const readme = [
    '# Obsite UI design captures',
    '',
    `Generated ${new Date().toISOString()} with Chromium at ${staticOrigin} (public) and ${editorOrigin} (editor).`,
    '',
    ...manifest.map(item => `- [${item.file}](./${item.file}) — ${item.description}`),
    '',
    'The captures cover generated public pages, responsive and dark states, the login screen, the authenticated editor, source mode, preview, media library, and file-manager dialogs.'
  ];
  await fs.writeFile(path.join(outputDir, 'README.md'), readme.join('\n') + '\n');
  console.log(`Captured ${manifest.length} screenshots in ${outputDir}`);
} finally {
  if (browser) await browser.close();
  if (editorProcess && !editorProcess.killed) editorProcess.kill('SIGTERM');
  await waitForEditorExit();
  if (staticServer) await new Promise(resolve => staticServer.close(resolve));
  await fs.rm(tempRoot, {recursive: true, force: true});
}
