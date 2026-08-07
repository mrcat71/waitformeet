/**
 * Light and dark theme handling.
 *
 * The default follows the operating system. An explicit choice is remembered in
 * localStorage and stamped onto the root element as data-theme, which the stylesheet
 * treats as an override of the prefers-color-scheme default.
 */

const STORAGE_KEY = "wfm:theme";

export type Theme = "light" | "dark" | "system";

function isTheme(value: string | null): value is Theme {
  return value === "light" || value === "dark" || value === "system";
}

/** Reads the remembered choice. Private-mode browsers that throw fall back to system. */
export function storedTheme(): Theme {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    return isTheme(raw) ? raw : "system";
  } catch {
    return "system";
  }
}

function persist(theme: Theme): void {
  try {
    if (theme === "system") {
      window.localStorage.removeItem(STORAGE_KEY);
    } else {
      window.localStorage.setItem(STORAGE_KEY, theme);
    }
  } catch {
    // Storage being unavailable is not worth interrupting the page for; the
    // choice simply lasts until reload.
  }
}

/** Applies a theme to the document and remembers it. */
export function applyTheme(theme: Theme): void {
  const root = document.documentElement;
  if (theme === "system") {
    delete root.dataset["theme"];
  } else {
    root.dataset["theme"] = theme;
  }
  persist(theme);

  for (const button of document.querySelectorAll<HTMLElement>("[data-theme-toggle]")) {
    button.dataset["themeState"] = theme;
    button.setAttribute("aria-pressed", String(theme === "dark"));
  }
}

/** Resolves what the page is actually showing right now. */
export function effectiveTheme(): "light" | "dark" {
  const explicit = storedTheme();
  if (explicit !== "system") {
    return explicit;
  }
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

export function initTheme(root: ParentNode): void {
  applyTheme(storedTheme());

  for (const button of root.querySelectorAll<HTMLElement>("[data-theme-toggle]")) {
    button.addEventListener("click", () => {
      applyTheme(effectiveTheme() === "dark" ? "light" : "dark");
    });
  }
}
