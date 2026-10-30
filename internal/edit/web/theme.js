(function () {
  "use strict";

  var root = document.documentElement;
  var storageKey = "oxpio.theme.v1";
  var media = window.matchMedia ? window.matchMedia("(prefers-color-scheme: dark)") : null;
  var labels = {system: "Follow system", light: "Light", dark: "Dark"};

  function isTheme(value) {
    return value === "system" || value === "light" || value === "dark";
  }

  function readStoredTheme() {
    try {
      var value = window.localStorage.getItem(storageKey);
      if (isTheme(value)) {
        return value;
      }
      var legacy = JSON.parse(window.localStorage.getItem("oxpio-editor-preferences") || "{}");
      if (isTheme(legacy.theme)) {
        window.localStorage.setItem(storageKey, legacy.theme);
        return legacy.theme;
      }
    } catch (error) {
      if (window.console && typeof window.console.warn === "function") {
        window.console.warn("[oxpio] Unable to read the saved color mode.", error);
      }
    }
    return "system";
  }

  function resolvedTheme(preference) {
    return preference === "dark" || (preference === "system" && media && media.matches) ? "dark" : "light";
  }

  function syncThemeControls(preference) {
    var controls = document.querySelectorAll("[data-theme-select]");
    for (var index = 0; index < controls.length; index += 1) {
      controls[index].value = preference;
    }
  }

  function applyTheme(preference) {
    preference = isTheme(preference) ? preference : "system";
    var resolved = resolvedTheme(preference);
    if (preference === "system") {
      root.removeAttribute("data-theme");
    } else {
      root.setAttribute("data-theme", preference);
    }
    root.style.colorScheme = resolved;
    return resolved;
  }

  function setTheme(preference) {
    preference = isTheme(preference) ? preference : "system";
    try {
      window.localStorage.setItem(storageKey, preference);
    } catch (error) {
      if (window.console && typeof window.console.warn === "function") {
        window.console.warn("[oxpio] Unable to save the selected color mode.", error);
      }
    }
    applyTheme(preference);
    syncThemeControls(preference);
  }

  function bindThemeControls() {
    var controls = document.querySelectorAll("[data-theme-select]");
    var preference = readStoredTheme();
    for (var index = 0; index < controls.length; index += 1) {
      var control = controls[index];
      control.value = preference;
      if (control.getAttribute("data-theme-ready") === "true") {
        continue;
      }
      control.setAttribute("data-theme-ready", "true");
      control.addEventListener("change", function (event) {
        setTheme(event.target.value);
      });
    }
  }

  function refreshSystemTheme() {
    var preference = readStoredTheme();
    if (preference === "system") {
      applyTheme(preference);
      syncThemeControls(preference);
    }
  }

  var initialPreference = readStoredTheme();
  applyTheme(initialPreference);
  window.OXPIOTheme = {
    getPreference: readStoredTheme,
    setPreference: setTheme,
    label: function (preference) { return labels[isTheme(preference) ? preference : "system"]; }
  };

  if (media) {
    if (typeof media.addEventListener === "function") {
      media.addEventListener("change", refreshSystemTheme);
    } else if (typeof media.addListener === "function") {
      media.addListener(refreshSystemTheme);
    }
  }
  window.addEventListener("storage", function (event) {
    if (event.key !== storageKey) {
      return;
    }
    var preference = isTheme(event.newValue) ? event.newValue : "system";
    applyTheme(preference);
    syncThemeControls(preference);
  });
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", bindThemeControls, {once: true});
  } else {
    bindThemeControls();
  }
})();
