/**
 * Page entry point.
 *
 * Everything here is progressive enhancement: the server renders a usable page and
 * these widgets make it live. Nothing on the page depends on this script running.
 */

import { initClocks } from "./clocks.js";
import { initCelebration } from "./confetti.js";
import { initCountdown, initProgress } from "./countdown.js";
import { initLightbox } from "./lightbox.js";
import { initTheme } from "./theme.js";
import { ServerClock, parseInstant } from "./time.js";

function pageLocale(): string {
  return document.documentElement.lang || "en";
}

/**
 * Builds a clock anchored to the server's time.
 *
 * The server stamps data-server-now onto <body>. Without it we fall back to the
 * browser clock, which is fine for a single visitor but can disagree with what the
 * other person sees.
 */
function serverClock(): ServerClock {
  const stamped = parseInstant(document.body.dataset["serverNow"]);
  return new ServerClock(stamped ?? Date.now());
}

function registerServiceWorker(): void {
  if (!("serviceWorker" in navigator)) {
    return;
  }
  const url = document.body.dataset["swUrl"];
  if (!url) {
    return;
  }
  window.addEventListener("load", () => {
    navigator.serviceWorker.register(url).catch((err: unknown) => {
      // Registration failing (http, private mode, blocked) must not break the page.
      console.warn("waitformeet: service worker registration failed", err);
    });
  });
}

function main(): void {
  const locale = pageLocale();
  const clock = serverClock();

  initTheme(document);
  initCountdown(document, clock, locale);
  initProgress(document, clock, locale);
  initClocks(document, clock, locale);
  initLightbox(document);
  initCelebration(document);
  registerServiceWorker();
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", main, { once: true });
} else {
  main();
}
