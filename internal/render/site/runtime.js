(function () {
  "use strict";

  var root = document.documentElement;
  var basePath = root.getAttribute("data-oxpio-base-path") || "/";
  var storageKey = "oxpio.theme.v1:" + basePath;
  var media = window.matchMedia ? window.matchMedia("(prefers-color-scheme: dark)") : null;
  var runtimeScript = document.currentScript;
  var vendorBaseURL = runtimeScript && runtimeScript.src ? new URL("../oxpio-runtime/", runtimeScript.src) : null;
  var servedSiteRootURL = runtimeScript && runtimeScript.src ? new URL("../../", runtimeScript.src) : null;

  function report(level, message, error) {
    var logger = window.console && window.console[level];
    if (typeof logger === "function") {
      logger.call(window.console, "[oxpio] " + message, error || "");
    }
  }

  function isStoredTheme(value) {
    return value === "light" || value === "dark";
  }

  function readStoredTheme() {
    try {
      var value = window.localStorage.getItem(storageKey);
      return isStoredTheme(value) ? value : "";
    } catch (error) {
      report("warn", "Unable to read the saved color mode.", error);
      return "";
    }
  }

  function resolveTheme(preference) {
    if (isStoredTheme(preference)) {
      return preference;
    }
    return media && media.matches ? "dark" : "light";
  }

  function applyTheme(preference) {
    var resolved = resolveTheme(preference);
    if (isStoredTheme(preference)) {
      root.setAttribute("data-theme", preference);
    } else {
      root.removeAttribute("data-theme");
    }
    root.style.colorScheme = resolved;
    return resolved;
  }

  function setToggleText(toggle, selector, value) {
    var element = toggle.querySelector(selector);
    if (element) {
      element.textContent = value;
    }
  }

  function syncThemeToggle(toggle, preference) {
    if (!toggle) {
      return;
    }
    var resolved = resolveTheme(preference);
    setToggleText(toggle, "[data-theme-toggle-value]", resolved === "dark" ? "Dark" : "Light");
    setToggleText(toggle, "[data-theme-toggle-state]", "Current mode " + resolved + ".");
    setToggleText(toggle, "[data-theme-toggle-source]", preference ? "Theme locked to " + preference + "." : "Following system preference.");
    toggle.setAttribute("aria-pressed", String(resolved === "dark"));
    toggle.hidden = false;
  }

  function writeStoredTheme(value) {
    try {
      window.localStorage.setItem(storageKey, value);
    } catch (error) {
      report("warn", "Unable to save the selected color mode.", error);
    }
  }

  function initThemeToggle() {
    var toggle = document.querySelector("[data-theme-toggle]");
    if (!toggle || toggle.getAttribute("data-theme-toggle-ready") === "true") {
      return;
    }
    toggle.setAttribute("data-theme-toggle-ready", "true");
    toggle.addEventListener("click", function () {
      var next = resolveTheme(readStoredTheme()) === "dark" ? "light" : "dark";
      writeStoredTheme(next);
      applyTheme(next);
      syncThemeToggle(toggle, next);
    });
    syncThemeToggle(toggle, readStoredTheme());
  }

  function onReady(callback) {
    if (document.readyState === "loading") {
      document.addEventListener("DOMContentLoaded", callback, {once: true});
    } else {
      callback();
    }
  }

  function loadScript(url, label) {
    return new Promise(function (resolve, reject) {
      var script = document.createElement("script");
      script.src = url;
      script.defer = true;
      script.addEventListener("load", resolve, {once: true});
      script.addEventListener("error", function () {
        reject(new Error("Failed to load " + label + " from " + url));
      }, {once: true});
      document.head.appendChild(script);
    });
  }

  function initSidebar() {
    if (!root.hasAttribute("data-oxpio-sidebar") || !runtimeScript || !runtimeScript.src) {
      return;
    }

    var shell = document.querySelector("[data-sidebar-shell]");
    var sidebarRoot = document.querySelector("[data-sidebar-root]");
    var siteBody = document.querySelector("[data-site-body]");
    var toggle = document.querySelector("[data-sidebar-toggle]");
    var closeButton = document.querySelector("[data-sidebar-close]");
    var overlay = document.querySelector("[data-sidebar-overlay]");
    var media = window.matchMedia ? window.matchMedia("(max-width: 56rem)") : null;
    var storageKey = "oxpio.sidebar.expanded.v1:" + basePath;
    var expanded = {};
    var currentPath = currentSitePath();
    var popoverEnabled = root.hasAttribute("data-oxpio-popover");

    if (!shell || !sidebarRoot || !siteBody) {
      report("error", "Sidebar mount points are unavailable.");
      return;
    }

    window.fetch(new URL("sidebar.json", runtimeScript.src).href, {
      cache: "no-cache",
      headers: {Accept: "application/json"}
    }).then(function (response) {
      if (!response.ok) {
        throw new Error("Sidebar data returned HTTP " + response.status);
      }
      return response.json();
    }).then(function (payload) {
      var nodes = payload;
      if (payload && !Array.isArray(payload) && typeof payload === "object") {
        var versionID = root.getAttribute("data-oxpio-version") || "";
        nodes = versionID && payload.versions && Array.isArray(payload.versions[versionID]) ? payload.versions[versionID] : payload.default;
      }
      if (!Array.isArray(nodes)) {
        throw new Error("Sidebar data is not an array or versioned sidebar payload");
      }
      if (!nodes.length) {
        return;
      }

      expanded = readExpandedState();
      sidebarRoot.textContent = "";
      sidebarRoot.appendChild(renderList(nodes, true));
      shell.hidden = false;
      siteBody.setAttribute("data-sidebar-ready", "true");

      if (toggle) {
        toggle.hidden = false;
        toggle.addEventListener("click", function () {
          setMobileSidebarOpen(!isMobileSidebarOpen());
        });
      }
      if (closeButton) {
        closeButton.addEventListener("click", function () {
          setMobileSidebarOpen(false);
        });
      }
      if (overlay) {
        overlay.addEventListener("click", function () {
          setMobileSidebarOpen(false);
        });
      }
      sidebarRoot.addEventListener("click", function (event) {
        var link = event.target.closest("a.sidebar-link");
        if (link && isMobileViewport()) {
          setMobileSidebarOpen(false);
        }
      });
      document.addEventListener("keydown", function (event) {
        if (event.key === "Escape") {
          setMobileSidebarOpen(false);
        }
      });
      if (media && typeof media.addEventListener === "function") {
        media.addEventListener("change", syncViewportState);
      } else if (media && typeof media.addListener === "function") {
        media.addListener(syncViewportState);
      }
      syncViewportState();
    }).catch(function (error) {
      report("error", "Sidebar initialization failed.", error);
    });

    function renderList(items, isRoot) {
      var list = document.createElement("ul");
      list.className = isRoot ? "sidebar-list sidebar-list-root" : "sidebar-list";
      for (var index = 0; index < items.length; index += 1) {
        var node = items[index];
        if (node && typeof node.name === "string" && typeof node.url === "string") {
          list.appendChild(renderNode(node));
        }
      }
      return list;
    }

    function renderNode(node) {
      var item = document.createElement("li");
      var row = document.createElement("div");
      var link = document.createElement("a");
      var children = Array.isArray(node.children) ? node.children : [];
      var isDirectory = node.isDir === true;
      var isExpandable = isDirectory && children.length > 0;
      var key = stableNodeKey(node);
      var isCurrent = key !== "" && key === currentPath;
      var hasStoredExpanded = Object.prototype.hasOwnProperty.call(expanded, key);
      var isExpanded = hasStoredExpanded ? expanded[key] === true : nodeHasCurrent(node);

      item.className = isDirectory ? "sidebar-node sidebar-node-dir" : "sidebar-node sidebar-node-file";
      if (isCurrent) {
        item.classList.add("is-active");
      }
      if (isExpandable) {
        item.setAttribute("data-expanded", String(isExpanded));
      }

      row.className = "sidebar-item";
      row.appendChild(isExpandable ? renderToggle(node, item, isExpanded) : renderToggleSpacer());
      link.className = isDirectory ? "sidebar-link sidebar-link-dir" : "sidebar-link";
      link.href = buildNodeHref(node.url);
      link.textContent = node.name;
      if (popoverEnabled && !isDirectory) {
        link.setAttribute("data-popover-path", node.source || key);
      }
      if (isCurrent) {
        link.classList.add("is-current");
        link.setAttribute("aria-current", "page");
      }
      row.appendChild(link);
      item.appendChild(row);

      if (isExpandable) {
        var branch = renderList(children, false);
        branch.hidden = !isExpanded;
        item.appendChild(branch);
      }
      return item;
    }

    function renderToggle(node, item, isExpanded) {
      var button = document.createElement("button");
      var glyph = document.createElement("span");
      var srText = document.createElement("span");
      button.className = "sidebar-toggle";
      button.type = "button";
      button.setAttribute("aria-expanded", String(isExpanded));
      glyph.className = "sidebar-toggle-glyph";
      glyph.setAttribute("aria-hidden", "true");
      glyph.textContent = "▾";
      srText.className = "sr-only";
      srText.textContent = "Toggle " + node.name;
      button.appendChild(glyph);
      button.appendChild(srText);
      button.addEventListener("click", function () {
        var nextExpanded = item.getAttribute("data-expanded") !== "true";
        var branch = item.querySelector(":scope > ul");
        item.setAttribute("data-expanded", String(nextExpanded));
        button.setAttribute("aria-expanded", String(nextExpanded));
        if (branch) {
          branch.hidden = !nextExpanded;
        }
        expanded[stableNodeKey(node)] = nextExpanded;
        persistExpandedState();
      });
      return button;
    }

    function renderToggleSpacer() {
      var spacer = document.createElement("span");
      spacer.className = "sidebar-toggle sidebar-toggle-spacer";
      spacer.setAttribute("aria-hidden", "true");
      return spacer;
    }

    function normalizeSitePath(value) {
      if (typeof value !== "string") {
        return "";
      }
      try {
        value = decodeURIComponent(value);
      } catch (error) {
        report("warn", "Unable to decode a Sidebar path.", error);
      }
      return value.replace(/^\/+|\/+$/g, "");
    }

    function stableNodeKey(node) {
      return normalizeSitePath(node && node.url);
    }

    function encodedSitePath(value) {
      if (typeof value !== "string") {
        return "";
      }
      return value.replace(/^\/+|\/+$/g, "");
    }

    function buildNodeHref(rawURL) {
      var cleanURL = encodedSitePath(rawURL);
      if (!servedSiteRootURL) {
        return cleanURL ? (basePath + cleanURL + "/") : basePath;
      }
      return new URL(cleanURL ? cleanURL + "/" : "./", servedSiteRootURL).href;
    }

    function currentSitePath() {
      var pathname = window.location && window.location.pathname ? window.location.pathname : "/";
      try {
        pathname = decodeURIComponent(pathname);
      } catch (error) {
        report("warn", "Unable to decode the current page URL.", error);
      }
      var cleanBase = servedSiteRootURL && servedSiteRootURL.pathname ? servedSiteRootURL.pathname : basePath;
      try {
        cleanBase = decodeURIComponent(cleanBase);
      } catch (error) {
        report("warn", "Unable to decode the site base path.", error);
      }
      if (pathname.indexOf(cleanBase) === 0) {
        pathname = pathname.slice(cleanBase.length);
      }
      pathname = encodedSitePath(pathname);
      if (pathname === "index.html") {
        return "";
      }
      pathname = pathname.replace(/\/index\.html$/, "");
      return pathname.replace(/\/page\/[1-9][0-9]*$/, "");
    }

    function nodeHasCurrent(node) {
      if (stableNodeKey(node) === currentPath) {
        return true;
      }
      var children = Array.isArray(node && node.children) ? node.children : [];
      for (var index = 0; index < children.length; index += 1) {
        if (nodeHasCurrent(children[index])) {
          return true;
        }
      }
      return false;
    }

    function readExpandedState() {
      var raw = "";
      try {
        raw = window.localStorage.getItem(storageKey) || "";
      } catch (error) {
        report("warn", "Unable to read Sidebar state.", error);
        return {};
      }
      if (!raw) {
        return {};
      }
      try {
        var parsed = JSON.parse(raw);
        if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
          return parsed;
        }
      } catch (error) {
        report("warn", "Unable to parse Sidebar state.", error);
      }
      return {};
    }

    function persistExpandedState() {
      try {
        window.localStorage.setItem(storageKey, JSON.stringify(expanded));
      } catch (error) {
        report("warn", "Unable to save Sidebar state.", error);
      }
    }

    function isMobileViewport() {
      return !!(media && media.matches);
    }

    function isMobileSidebarOpen() {
      return document.body.getAttribute("data-sidebar-open") === "true";
    }

    function syncSidebarAccessibility(open) {
      var accessible = open || !isMobileViewport();
      if (accessible) {
        shell.removeAttribute("aria-hidden");
        shell.removeAttribute("inert");
        if ("inert" in shell) {
          shell.inert = false;
        }
      } else {
        shell.setAttribute("aria-hidden", "true");
        shell.setAttribute("inert", "");
        if ("inert" in shell) {
          shell.inert = true;
        }
      }
    }

    function setMobileSidebarOpen(open) {
      if (!isMobileViewport()) {
        open = false;
      }
      if (open) {
        document.body.setAttribute("data-sidebar-open", "true");
      } else {
        document.body.removeAttribute("data-sidebar-open");
      }
      syncSidebarAccessibility(open);
      if (overlay) {
        overlay.hidden = !open;
      }
      if (toggle) {
        toggle.setAttribute("aria-expanded", String(open));
      }
    }

    function syncViewportState() {
      setMobileSidebarOpen(false);
    }
  }

  function initPopover() {
    if (!root.hasAttribute("data-oxpio-popover")) {
      return;
    }
    var card = document.querySelector("[data-popover-card]");
    if (!card || !servedSiteRootURL) {
      report("error", "Popover mount point is unavailable.");
      return;
    }

    var cache = new Map();
    var showTimer = 0;
    var hideTimer = 0;
    var activeLink = null;
    var activeDescribedBy = null;
    var hoveredLink = null;
    var focusedLink = null;
    var cardHovered = false;
    var cardFocused = false;
    var activeRequest = 0;

    function popoverLink(target) {
      return target && typeof target.closest === "function" ? target.closest("[data-popover-path]") : null;
    }

    function clearTimers() {
      window.clearTimeout(showTimer);
      window.clearTimeout(hideTimer);
    }

    function restoreActiveDescription() {
      if (!activeLink) {
        activeDescribedBy = null;
        return;
      }
      if (activeDescribedBy === null) {
        activeLink.removeAttribute("aria-describedby");
      } else {
        activeLink.setAttribute("aria-describedby", activeDescribedBy);
      }
      activeDescribedBy = null;
    }

    function hidePopover() {
      clearTimers();
      restoreActiveDescription();
      activeLink = null;
      activeRequest += 1;
      card.hidden = true;
      card.setAttribute("aria-hidden", "true");
      card.textContent = "";
      card.style.left = "";
      card.style.top = "";
    }

    function scheduleHideIfIdle() {
      if (hoveredLink || focusedLink || cardHovered || cardFocused) {
        window.clearTimeout(hideTimer);
        return;
      }
      window.clearTimeout(hideTimer);
      hideTimer = window.setTimeout(hidePopover, 90);
    }

    function syncPopoverTarget() {
      var nextLink = focusedLink || hoveredLink;
      if (nextLink) {
        window.clearTimeout(hideTimer);
        if (activeLink !== nextLink || card.hidden) {
          showPopover(nextLink);
        }
        return;
      }
      scheduleHideIfIdle();
    }

    function transitionInside(link, relatedTarget) {
      return !!(link && relatedTarget && typeof link.contains === "function" && link.contains(relatedTarget));
    }

    function resolvePopoverURL(link) {
      var notePath = link ? (link.getAttribute("data-popover-path") || "").replace(/^\/+|\/+$/g, "") : "";
      if (!notePath || notePath.indexOf("\\") !== -1 || notePath.split("/").some(function (segment) { return !segment || segment === "." || segment === ".."; })) {
        return "";
      }
      var encoded = notePath.split("/").map(encodeRFC3986SourceSegment).join("/");
      return new URL("_popover/" + encoded + "/index.json", servedSiteRootURL).href;
    }

    function encodeRFC3986SourceSegment(value) {
      return encodeURIComponent(value).replace(/[!'()*]/g, function (character) {
        return "%" + character.charCodeAt(0).toString(16).toUpperCase();
      });
    }

    function encodeRFC3986Segment(value) {
      if (typeof value.normalize === "function") {
        value = value.normalize("NFKC");
      }
      return encodeURIComponent(value).replace(/[!'()*]/g, function (character) {
        return "%" + character.charCodeAt(0).toString(16).toUpperCase();
      });
    }

    function loadPopover(popoverURL) {
      if (cache.has(popoverURL)) {
        return cache.get(popoverURL);
      }
      var pending = window.fetch(popoverURL, {
        headers: {Accept: "application/json"}
      }).then(function (response) {
        if (!response.ok) {
          throw new Error("Popover data returned HTTP " + response.status);
        }
        return response.json();
      }).then(function (payload) {
        if (!payload || typeof payload.title !== "string" || typeof payload.summary !== "string" || !Array.isArray(payload.tags)) {
          throw new Error("Popover data has an invalid shape");
        }
        return {
          title: payload.title,
          summary: payload.summary,
          tags: payload.tags.filter(function (tag) { return typeof tag === "string" && tag.trim() !== ""; })
        };
      }).catch(function (error) {
        report("error", "Popover request failed.", error);
        return null;
      });
      cache.set(popoverURL, pending);
      return pending;
    }

    function renderPopover(payload) {
      if (!payload || !payload.title) {
        hidePopover();
        return false;
      }
      card.textContent = "";
      var title = document.createElement("p");
      title.className = "popover-card-title";
      title.textContent = payload.title;
      card.appendChild(title);
      if (payload.summary) {
        var summary = document.createElement("p");
        summary.className = "popover-card-summary";
        summary.textContent = payload.summary;
        card.appendChild(summary);
      }
      if (payload.tags.length) {
        var tags = document.createElement("div");
        tags.className = "popover-card-tags";
        for (var index = 0; index < payload.tags.length; index += 1) {
          var pill = document.createElement("span");
          pill.className = "popover-card-tag";
          pill.textContent = "#" + payload.tags[index];
          tags.appendChild(pill);
        }
        card.appendChild(tags);
      }
      card.style.left = "-9999px";
      card.style.top = "0";
      card.hidden = false;
      card.setAttribute("aria-hidden", "false");
      return true;
    }

    function positionPopover(link) {
      if (!link || card.hidden) {
        return;
      }
      var rect = link.getBoundingClientRect();
      var gap = 14;
      var viewportPadding = 16;
      var left = rect.left;
      var top = rect.bottom + gap;
      if (left + card.offsetWidth > window.innerWidth - viewportPadding) {
        left = window.innerWidth - card.offsetWidth - viewportPadding;
      }
      if (left < viewportPadding) {
        left = viewportPadding;
      }
      if (top + card.offsetHeight > window.innerHeight - viewportPadding) {
        top = rect.top - card.offsetHeight - gap;
      }
      if (top < viewportPadding) {
        top = viewportPadding;
      }
      card.style.left = Math.round(left) + "px";
      card.style.top = Math.round(top) + "px";
    }

    function showPopover(link) {
      var popoverURL = resolvePopoverURL(link);
      if (!popoverURL) {
        return;
      }
      if (activeLink !== link) {
        restoreActiveDescription();
        cardHovered = false;
        cardFocused = false;
        activeLink = link;
        activeDescribedBy = link.getAttribute("aria-describedby");
      }
      activeRequest += 1;
      var requestID = activeRequest;
      window.clearTimeout(hideTimer);
      window.clearTimeout(showTimer);
      card.hidden = true;
      card.setAttribute("aria-hidden", "true");
      card.textContent = "";
      showTimer = window.setTimeout(function () {
        loadPopover(popoverURL).then(function (payload) {
          if (requestID !== activeRequest || activeLink !== link || !renderPopover(payload)) {
            return;
          }
          var descriptions = activeDescribedBy ? activeDescribedBy.trim().split(/\s+/) : [];
          if (descriptions.indexOf(card.id) === -1) {
            descriptions.push(card.id);
          }
          link.setAttribute("aria-describedby", descriptions.join(" "));
          positionPopover(link);
        });
      }, 150);
    }

    document.addEventListener("mouseover", function (event) {
      var link = popoverLink(event.target);
      if (link && !transitionInside(link, event.relatedTarget)) {
        hoveredLink = link;
        syncPopoverTarget();
      }
    });
    document.addEventListener("mouseout", function (event) {
      var link = popoverLink(event.target);
      if (link && !transitionInside(link, event.relatedTarget) && hoveredLink === link) {
        hoveredLink = null;
        syncPopoverTarget();
      }
    });
    document.addEventListener("focusin", function (event) {
      var link = popoverLink(event.target);
      if (link && !transitionInside(link, event.relatedTarget)) {
        focusedLink = link;
        syncPopoverTarget();
      }
    });
    document.addEventListener("focusout", function (event) {
      var link = popoverLink(event.target);
      if (link && !transitionInside(link, event.relatedTarget) && focusedLink === link) {
        focusedLink = null;
        syncPopoverTarget();
      }
    });
    card.addEventListener("mouseenter", function () { cardHovered = true; window.clearTimeout(hideTimer); });
    card.addEventListener("mouseleave", function () { cardHovered = false; scheduleHideIfIdle(); });
    card.addEventListener("focusin", function () { cardFocused = true; window.clearTimeout(hideTimer); });
    card.addEventListener("focusout", function (event) {
      if (!event.relatedTarget || !card.contains(event.relatedTarget)) {
        cardFocused = false;
        scheduleHideIfIdle();
      }
    });
    document.addEventListener("keydown", function (event) {
      if (event.key === "Escape") {
        hoveredLink = null;
        focusedLink = null;
        cardHovered = false;
        cardFocused = false;
        hidePopover();
      }
    });
    window.addEventListener("scroll", function () { if (activeLink && !card.hidden) positionPopover(activeLink); });
    window.addEventListener("resize", function () { if (activeLink && !card.hidden) positionPopover(activeLink); });
  }

  function initMath() {
    if (!root.hasAttribute("data-oxpio-math") || !vendorBaseURL) {
      return;
    }
    loadScript(new URL("katex.min.js", vendorBaseURL).href, "KaTeX")
      .then(function () {
        return loadScript(new URL("auto-render.min.js", vendorBaseURL).href, "KaTeX auto-render");
      })
      .then(function () {
        var target = document.querySelector("[data-page-content]");
        if (!target || typeof window.renderMathInElement !== "function") {
          throw new Error("KaTeX auto-render API is unavailable");
        }
        window.renderMathInElement(target, {
          delimiters: [
            {left: "$$", right: "$$", display: true},
            {left: "\\[", right: "\\]", display: true},
            {left: "$", right: "$", display: false},
            {left: "\\(", right: "\\)", display: false}
          ],
          throwOnError: false,
          errorCallback: function (message, error) {
            report("error", "KaTeX could not render formula: " + message, error);
          }
        });
        var sources = target.querySelectorAll("[data-oxpio-math-source]");
        for (var index = 0; index < sources.length; index += 1) {
          if (!sources[index].querySelector(".katex") && !sources[index].querySelector(".katex-error")) {
            report("error", "KaTeX could not render formula: " + sources[index].textContent);
          }
        }
        var failures = target.querySelectorAll(".katex-error");
        for (var failureIndex = 0; failureIndex < failures.length; failureIndex += 1) {
          report("error", "KaTeX could not render formula: " + failures[failureIndex].textContent);
        }
      })
      .catch(function (error) {
        report("error", "KaTeX initialization failed.", error);
      });
  }

  function initMermaid() {
    if (!root.hasAttribute("data-oxpio-mermaid") || !vendorBaseURL) {
      return;
    }
    loadScript(new URL("mermaid.min.js", vendorBaseURL).href, "Mermaid")
      .then(function () {
        if (!window.mermaid || typeof window.mermaid.run !== "function") {
          throw new Error("Mermaid browser API is unavailable");
        }
        window.mermaid.initialize({startOnLoad: false, theme: "neutral", securityLevel: "loose"});
        var blocks = document.querySelectorAll("pre.mermaid");
        var sequence = Promise.resolve();
        for (var index = 0; index < blocks.length; index += 1) {
          (function (block) {
            sequence = sequence.then(function () {
              var source = block.textContent;
              return Promise.resolve(window.mermaid.run({nodes: [block], suppressErrors: false}))
                .catch(function (error) {
                  if (!block.querySelector("svg")) {
                    block.textContent = source;
                    block.removeAttribute("data-processed");
                  }
                  report("error", "Mermaid could not render diagram.", error);
                });
            });
          })(blocks[index]);
        }
        return sequence;
      })
      .catch(function (error) {
        report("error", "Mermaid initialization failed.", error);
      });
  }

  var initialPreference = readStoredTheme();
  applyTheme(initialPreference);

  if (media) {
    var handleSystemThemeChange = function () {
      if (!readStoredTheme()) {
        applyTheme("");
        syncThemeToggle(document.querySelector("[data-theme-toggle]"), "");
      }
    };
    if (typeof media.addEventListener === "function") {
      media.addEventListener("change", handleSystemThemeChange);
    } else if (typeof media.addListener === "function") {
      media.addListener(handleSystemThemeChange);
    }
  }

  onReady(function () {
    initThemeToggle();
    initSidebar();
    initPopover();
    initMath();
    initMermaid();
  });
})();
