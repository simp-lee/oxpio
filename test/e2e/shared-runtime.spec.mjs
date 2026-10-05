import {test, expect} from '@playwright/test';
import {spawn, execFileSync} from 'node:child_process';
import {createServer} from 'node:http';
import {promises as fs} from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const repoRoot = path.resolve(import.meta.dirname, '..', '..');
const fixtureRoot = path.join(repoRoot, 'test', 'testdata', 'e2e', 'runtime-vault');
const childDocumentRoutes = [
  '/alpha/child/intro/',
  '/alpha/child/tie-a/',
  '/alpha/child/tie-b/',
  '/alpha/child/child/'
];
let tempRoot;
let binaryPath;
let alphaVault;
let betaVault;
let staticServer;
let origin;
const requests = [];

async function listen(server, port = 0) {
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(port, '127.0.0.1', resolve);
  });
  return server.address().port;
}

function contentType(filePath) {
  switch (path.extname(filePath)) {
    case '.html': return 'text/html; charset=utf-8';
    case '.js': return 'text/javascript; charset=utf-8';
    case '.css': return 'text/css; charset=utf-8';
    case '.json': return 'application/json; charset=utf-8';
    case '.png': return 'image/png';
    case '.woff2': return 'font/woff2';
    case '.woff': return 'font/woff';
    case '.ttf': return 'font/ttf';
    default: return 'application/octet-stream';
  }
}

async function serveOutput(req, res) {
  const url = new URL(req.url, 'http://127.0.0.1');
  requests.push({path: url.pathname, cacheControl: req.headers['cache-control'] || ''});
  const match = /^\/(alpha|beta)(\/.*)?$/.exec(url.pathname);
  if (!match) {
    res.writeHead(404).end('not found');
    return;
  }
  const outputRoot = path.join(match[1] === 'alpha' ? alphaVault : betaVault, 'public');
  let relative = (match[2] || '/').replace(/^\/+/, '');
  try {
    relative = decodeURIComponent(relative);
  } catch {
    res.writeHead(400).end('bad path');
    return;
  }
  if (!relative || relative.endsWith('/')) relative += 'index.html';
  const filePath = path.resolve(outputRoot, relative);
  if (filePath !== outputRoot && !filePath.startsWith(outputRoot + path.sep)) {
    res.writeHead(403).end('forbidden');
    return;
  }
  try {
    const data = await fs.readFile(filePath);
    res.writeHead(200, {'content-type': contentType(filePath)}).end(data);
  } catch {
    res.writeHead(404).end('not found');
  }
}

async function copyAndBuild(name, basePath) {
  const vault = path.join(tempRoot, name);
  await fs.cp(fixtureRoot, vault, {recursive: true});
  const configPath = path.join(vault, 'oxpio.yaml');
  const config = (await fs.readFile(configPath, 'utf8')).replace('http://127.0.0.1/alpha/', `${origin}${basePath}`);
  await fs.writeFile(configPath, config);
  execFileSync(binaryPath, ['build', '--vault', vault], {cwd: repoRoot, stdio: 'inherit'});
  return vault;
}

async function installEncodedSidebarFixture(vault) {
  const publicRoot = path.join(vault, 'public');
  const sourceHTML = await fs.readFile(path.join(publicRoot, 'child', 'child', 'index.html'), 'utf8');
  for (const fixtureName of ['a%20b', ' note', 'note']) {
    const fixturePath = path.join(publicRoot, fixtureName);
    await fs.mkdir(fixturePath, {recursive: true});
    await fs.writeFile(path.join(fixturePath, 'index.html'), sourceHTML);
  }

  const sidebarPath = path.join(publicRoot, 'assets', 'oxpio', 'sidebar.json');
  const sidebar = JSON.parse(await fs.readFile(sidebarPath, 'utf8'));
  sidebar.default.push(
    {name: 'Literal Percent', url: '/a%2520b/'},
    {name: 'Space', url: '/a%20b/'},
    {name: 'Leading Space', url: '/%20note/', source: ' note.md'},
    {name: 'No Leading Space', url: '/note/'},
    {name: 'Fullwidth Source', url: '/fullwidth-source/', source: 'Ａ.md'}
  );
  await fs.writeFile(sidebarPath, JSON.stringify(sidebar));

  for (const [source, title] of [[' note.md', 'Leading-space popover'], ['Ａ.md', 'Fullwidth popover']]) {
    const popoverPath = path.join(publicRoot, '_popover', source);
    await fs.mkdir(popoverPath, {recursive: true});
    await fs.writeFile(path.join(popoverPath, 'index.json'), JSON.stringify({title, summary: '', tags: []}));
  }
}

async function offlineContext(browser, options = {}, allowedOrigin = origin) {
  const context = await browser.newContext(options);
  const blocked = [];
  await context.route('**/*', async route => {
    const target = new URL(route.request().url());
    if (target.origin === allowedOrigin) {
      await route.continue();
    } else {
      blocked.push(target.href);
      await route.abort('blockedbyclient');
    }
  });
  return {context, blocked};
}

async function waitForHTTP(url, timeout = 20_000) {
  const deadline = Date.now() + timeout;
  let lastError;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url);
      if (response.ok) return;
      lastError = new Error(`HTTP ${response.status}`);
    } catch (error) {
      lastError = error;
    }
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw lastError || new Error(`timed out waiting for ${url}`);
}

async function glob(pattern) {
  const matches = [];
  for await (const match of fs.glob(pattern)) matches.push(match);
  return matches;
}

async function expectHrefOrder(locator, expected) {
  await expect(locator).toHaveCount(expected.length);
  for (const [index, href] of expected.entries()) {
    await expect(locator.nth(index)).toHaveAttribute('href', href);
  }
}

async function expectHrefSubsetOrder(root, expected) {
  const links = root.locator('a');
  const paths = async () => links.evaluateAll(anchors => anchors.map(anchor => {
    const href = anchor.getAttribute('href');
    try {
      return new URL(href, document.baseURI).pathname;
    } catch {
      return href;
    }
  }));
  await expect.poll(async () => {
    const hrefs = await paths();
    return hrefs.filter(href => expected.includes(href));
  }).toEqual(expected);
  for (const href of expected) {
    await expect.poll(async () => (await paths()).filter(path => path === href).length).toBe(1);
  }
}

async function freePort() {
  const server = createServer();
  const port = await listen(server);
  await new Promise(resolve => server.close(resolve));
  return port;
}

test.beforeAll(async () => {
  tempRoot = await fs.mkdtemp(path.join(os.tmpdir(), 'oxpio-e2e-'));
  binaryPath = path.join(tempRoot, process.platform === 'win32' ? 'oxpio.exe' : 'oxpio');
  execFileSync('go', ['build', '-o', binaryPath, './cmd/oxpio'], {cwd: repoRoot, stdio: 'inherit'});
  staticServer = createServer((req, res) => void serveOutput(req, res));
  const port = await listen(staticServer);
  origin = `http://127.0.0.1:${port}`;
  alphaVault = await copyAndBuild('alpha-vault', '/alpha/');
  betaVault = await copyAndBuild('beta-vault', '/beta/');
  await installEncodedSidebarFixture(alphaVault);
});

test.afterAll(async () => {
  if (staticServer) await new Promise(resolve => staticServer.close(resolve));
  if (tempRoot) await fs.rm(tempRoot, {recursive: true, force: true});
});

test('strict section pages and article flow remain usable without JavaScript', async ({browser}) => {
  const {context, blocked} = await offlineContext(browser, {javaScriptEnabled: false});
  const page = await context.newPage();
  await page.goto(`${origin}/alpha/child/`);
  await expect(page.getByRole('heading', {name: 'Nested Child'})).toBeVisible();
  await expect(page.locator('[data-theme-toggle]')).toBeHidden();
  await expect(page.locator('nav[aria-label="Global navigation"] a')).toHaveCount(2);
  await expect(page.locator('.breadcrumbs')).toContainText('Nested Child');
  const collection = page.locator('.section-articles a');
  await expectHrefOrder(collection, childDocumentRoutes);
  await expect(collection.locator('.listing-title')).toHaveText(['Intro', 'Tie', 'Tie', 'Child Article']);
  await expectHrefSubsetOrder(page.locator('[data-sidebar-root]'), childDocumentRoutes);

  const readingFlow = [
    {route: 'intro', position: '1 of 4', previous: null, next: 'tie-a'},
    {route: 'tie-a', position: '2 of 4', previous: 'intro', next: 'tie-b'},
    {route: 'tie-b', position: '3 of 4', previous: 'tie-a', next: 'child'},
    {route: 'child', position: '4 of 4', previous: 'tie-b', next: null}
  ];
  for (const item of readingFlow) {
    await page.goto(`${origin}/alpha/child/${item.route}/`);
    await expect(page.locator('.reading-flow .position')).toHaveText(item.position);
    const previous = page.locator('.reading-flow .previous');
    const next = page.locator('.reading-flow .next');
    await expect(previous).toHaveCount(item.previous ? 1 : 0);
    await expect(next).toHaveCount(item.next ? 1 : 0);
    if (item.previous) {
      await expect(previous).toHaveAttribute('href', `/alpha/child/${item.previous}/`);
    }
    if (item.next) {
      await expect(next).toHaveAttribute('href', `/alpha/child/${item.next}/`);
    }
  }
  await expect(page.locator('article > header h1')).toHaveText('Child Article');
  await expect(page.locator('.source-links')).toHaveCount(0);
  expect(blocked).toEqual([]);
  await context.close();
});

test('wide code and tables scroll locally on mobile viewports', async ({browser}) => {
  const {context, blocked} = await offlineContext(browser, {
    javaScriptEnabled: false,
    viewport: {width: 390, height: 844}
  });
  const page = await context.newPage();
  await page.goto(`${origin}/alpha/child/child/`);

  const metrics = await page.evaluate(() => {
    const documentRoot = document.documentElement;
    const content = document.querySelector('.entry-content');
    const pre = content.querySelector('pre');
    const table = content.querySelector('table');
    table.scrollLeft = 20;
    return {
      documentClientWidth: documentRoot.clientWidth,
      documentScrollWidth: documentRoot.scrollWidth,
      contentClientWidth: content.clientWidth,
      preClientWidth: pre.clientWidth,
      preScrollWidth: pre.scrollWidth,
      tableClientWidth: table.clientWidth,
      tableScrollWidth: table.scrollWidth,
      tableScrollLeft: table.scrollLeft,
      tableOverflowX: getComputedStyle(table).overflowX
    };
  });

  expect(metrics.documentScrollWidth).toBeLessThanOrEqual(metrics.documentClientWidth);
  expect(metrics.preClientWidth).toBeLessThanOrEqual(metrics.contentClientWidth);
  expect(metrics.tableClientWidth).toBeLessThanOrEqual(metrics.contentClientWidth);
  expect(metrics.preScrollWidth).toBeGreaterThan(metrics.preClientWidth);
  expect(metrics.tableScrollWidth).toBeGreaterThan(metrics.tableClientWidth);
  expect(metrics.tableScrollLeft).toBeGreaterThan(0);
  expect(metrics.tableOverflowX).toBe('auto');
  expect(blocked).toEqual([]);
  await context.close();
});

test('section banners, navigation activity, canonical URLs, and every social PNG are published', async ({browser}) => {
  const {context, blocked} = await offlineContext(browser, {javaScriptEnabled: false});
  const page = await context.newPage();
  await page.goto(`${origin}/alpha/child/`);
  await expect(page.locator('nav[aria-label="Global navigation"] a')).toHaveText(['Home', 'Child']);
  await expect(page.locator('nav[aria-label="Global navigation"] a').nth(0)).toHaveAttribute('aria-current', 'location');
  await expect(page.locator('nav[aria-label="Global navigation"] a').nth(1)).toHaveAttribute('aria-current', 'page');
  await expect(page.locator('img.page-banner')).toHaveAttribute('alt', 'Nested child banner');
  const sectionBannerURL = new URL(await page.locator('img.page-banner').getAttribute('src'), page.url());
  expect(sectionBannerURL.pathname).toMatch(/^\/alpha\/assets\/hero\.[a-f0-9]+\.png$/);
  const sectionBannerResponse = await page.request.get(sectionBannerURL.href);
  expect(sectionBannerResponse.status()).toBe(200);
  expect(sectionBannerResponse.headers()['content-type']).toBe('image/png');
  expect(await sectionBannerResponse.body()).toEqual(await fs.readFile(path.join(alphaVault, 'images', 'hero.png')));
  await expect(page.locator('link[rel="canonical"]')).toHaveAttribute('href', `${origin}/alpha/child/`);
  await page.goto(`${origin}/alpha/child/child/`);
  await expect(page.locator('nav[aria-label="Global navigation"] a').nth(0)).toHaveAttribute('aria-current', 'location');
  await expect(page.locator('nav[aria-label="Global navigation"] a').nth(1)).toHaveAttribute('aria-current', 'location');
  await expect(page.locator('img.page-banner')).toHaveAttribute('alt', 'Child article banner');
  const articleBannerURL = new URL(await page.locator('img.page-banner').getAttribute('src'), page.url());
  expect(articleBannerURL.href).toBe(sectionBannerURL.href);
  const articleBannerResponse = await page.request.get(articleBannerURL.href);
  expect(articleBannerResponse.status()).toBe(200);
  expect(articleBannerResponse.headers()['content-type']).toBe('image/png');
  expect(await articleBannerResponse.body()).toEqual(await sectionBannerResponse.body());
  await expect(page.locator('img.page-banner[alt="Nested child banner"]')).toHaveCount(0);
  await expect(page.locator('link[rel="canonical"]')).toHaveAttribute('href', `${origin}/alpha/child/child/`);
  const sitemap = await (await fetch(`${origin}/alpha/sitemap.xml`)).text();
  expect(sitemap).toContain(`${origin}/alpha/child/`);
  expect(sitemap).toContain(`${origin}/alpha/child/child/`);
  const social = await glob(path.join(alphaVault, 'public', 'assets', 'social', '*', '*.png'));
  expect(social.length).toBe(8);
  for (const file of social) {
    const assetPath = path.relative(path.join(alphaVault, 'public'), file).replaceAll(path.sep, '/');
    const response = await page.request.get(`${origin}/alpha/${assetPath}`);
    expect(response.status(), assetPath).toBe(200);
    expect(response.headers()['content-type'], assetPath).toBe('image/png');
    expect((await response.body()).subarray(0, 8), assetPath).toEqual(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]));
  }
  expect(blocked).toEqual([]);
  await context.close();
});

test('strict Markdown runtime stays local and social PNG is independently reachable', async ({browser}) => {
  const {context, blocked} = await offlineContext(browser);
  const page = await context.newPage();
  await page.goto(`${origin}/alpha/math-diagrams/`);
  await expect(page.locator('article > header h1')).toHaveText('Math and Diagrams');
  await expect(page.locator('[data-oxpio-main]')).toContainText('After invalid diagram remains visible.');
  await expect(page.locator('[data-oxpio-math-source] .katex')).toHaveCount(2);
  await expect(page.locator('[data-oxpio-math-source] .katex-error')).toHaveCount(0);
  await expect(page.locator('[data-page-content] pre.mermaid svg')).toHaveCount(1);
  await expect(page.locator('script[src*="/assets/oxpio/runtime."]')).toHaveCount(1);
  await page.goto(`${origin}/alpha/child/child/`);
  await expect.poll(() => page.locator('[data-site-body]').getAttribute('data-sidebar-ready')).toBe('true');
  await expect(page.locator('[data-sidebar-root] a[aria-current="page"]')).toHaveText('Child Article');
  const childDirectory = page.locator('[data-sidebar-root] .sidebar-node-dir').filter({hasText: 'Nested Child'}).first();
  const childToggle = childDirectory.locator(':scope > .sidebar-item > .sidebar-toggle');
  const childBranch = childDirectory.locator(':scope > .sidebar-list');
  await expect(childBranch).toBeVisible();
  await childToggle.click();
  await expect(childBranch).toBeHidden();
  await childToggle.click();
  await expect(childBranch).toBeVisible();
  await expectHrefSubsetOrder(page.locator('[data-sidebar-root]'), childDocumentRoutes);
  for (const href of childDocumentRoutes) {
    await expect(page.locator(`[data-sidebar-root] a[href$="${href}"]`)).toBeVisible();
  }
  const reference = page.locator('[data-page-content] a[data-popover-path="reference.md"]');
  await reference.focus();
  await expect.poll(() => page.locator('[data-popover-card]').getAttribute('aria-hidden')).toBe('false');
  await expect(page.locator('[data-popover-card]')).toContainText('Reference');
  const social = await glob(path.join(alphaVault, 'public', 'assets', 'social', '*', '*.png'));
  expect(social.length).toBeGreaterThanOrEqual(8);
  const response = await page.request.get(`${origin}/alpha/${path.relative(path.join(alphaVault, 'public'), social[0]).replaceAll(path.sep, '/')}`);
  expect(response.status()).toBe(200);
  expect(response.headers()['content-type']).toBe('image/png');
  expect((await response.body()).subarray(0, 8)).toEqual(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]));
  expect(blocked).toEqual([]);
  await context.close();
});

test('sidebar preserves encoded and whitespace-significant routes', async ({browser}) => {
  const {context, blocked} = await offlineContext(browser);
  const page = await context.newPage();
  await page.goto(`${origin}/alpha/a%2520b/`);
  await expect.poll(() => page.locator('[data-site-body]').getAttribute('data-sidebar-ready')).toBe('true');
  await expect(page.locator('[data-sidebar-root] a[aria-current="page"]')).toHaveText('Literal Percent');
  await expect(page.getByRole('link', {name: 'Space', exact: true})).not.toHaveAttribute('aria-current', 'page');

  await page.goto(`${origin}/alpha/%20note/`);
  await expect.poll(() => page.locator('[data-site-body]').getAttribute('data-sidebar-ready')).toBe('true');
  await expect(page.locator('[data-sidebar-root] a[aria-current="page"]')).toHaveText('Leading Space');
  await expect(page.getByRole('link', {name: 'No Leading Space', exact: true})).not.toHaveAttribute('aria-current', 'page');

  await page.getByRole('link', {name: 'Leading Space', exact: true}).focus();
  await expect(page.locator('[data-popover-card]')).toContainText('Leading-space popover');
  await page.getByRole('link', {name: 'Fullwidth Source', exact: true}).focus();
  await expect(page.locator('[data-popover-card]')).toContainText('Fullwidth popover');
  expect(blocked).toEqual([]);
  await context.close();
});

test('base-path builds are isolated and contain no external runtime requests', async ({browser}) => {
  const alpha = await fs.readFile(path.join(alphaVault, 'public', 'index.html'), 'utf8');
  const beta = await fs.readFile(path.join(betaVault, 'public', 'index.html'), 'utf8');
  expect(alpha).toContain(`${origin}/alpha/`);
  expect(alpha).not.toContain(`${origin}/beta/`);
  expect(beta).toContain(`${origin}/beta/`);
  expect(beta).not.toContain(`${origin}/alpha/`);
  const {context, blocked} = await offlineContext(browser);
  const page = await context.newPage();
  await page.goto(`${origin}/beta/`);
  await expect(page.getByRole('heading', {name: 'Runtime Home'})).toBeVisible();
  expect(blocked).toEqual([]);
  await context.close();
});

test('serve --watch rebuilds strict section content', async () => {
  const watchRoot = path.join(tempRoot, 'watch-vault');
  await fs.mkdir(path.join(watchRoot, 'docs'), {recursive: true});
  const port = await freePort();
  await fs.writeFile(path.join(watchRoot, 'oxpio.yaml'), `baseURL: http://127.0.0.1:${port}/watch/\ntitle: Watch Garden\nnavigation: []\n`);
  await fs.writeFile(path.join(watchRoot, '_index.md'), '---\ntitle: Watch Home\npublish: true\n---\nHome\n');
  const notePath = path.join(watchRoot, 'watch.md');
  await fs.writeFile(notePath, '---\ntitle: Watch Note\npublish: true\ntype: page\n---\nVersion one.\n');
  const childIndex = path.join(watchRoot, 'docs', '_index.md');
  await fs.writeFile(childIndex, '---\ntitle: Docs\npublish: true\n---\nDocs\n');
  const childNote = path.join(watchRoot, 'docs', 'guide.md');
  await fs.writeFile(childNote, '---\ntitle: Guide\npublish: true\ntype: doc\n---\nGuide one.\n');
  const child = spawn(binaryPath, ['serve', '--watch', '--vault', watchRoot, '--port', String(port)], {cwd: repoRoot, stdio: ['ignore', 'pipe', 'pipe']});
  let logs = '';
  child.stdout.on('data', chunk => { logs += chunk; });
  child.stderr.on('data', chunk => { logs += chunk; });
  try {
    await waitForHTTP(`http://127.0.0.1:${port}/watch/`);
    await fs.writeFile(childNote, '---\ntitle: Guide\npublish: true\ntype: doc\n---\nGuide two after rebuild.\n');
    await expect.poll(async () => fs.readFile(path.join(watchRoot, 'public', 'docs', 'guide', 'index.html'), 'utf8'), {timeout: 20_000, message: logs}).toContain('Guide two after rebuild.');
  } finally {
    if (child.exitCode === null) {
      child.kill('SIGTERM');
      await new Promise(resolve => child.once('exit', resolve));
    }
  }
  expect(logs).not.toContain('build failed');
});
