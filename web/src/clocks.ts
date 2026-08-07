/**
 * The two city clocks.
 *
 * Intl.DateTimeFormat with a timeZone option does all the hard work natively,
 * including daylight saving, so nothing here needs a timezone database.
 */

import type { ServerClock } from "./time.js";

/** Hours considered daytime in the target city, used only to pick sun or moon. */
const DAY_START_HOUR = 7;
const DAY_END_HOUR = 20;

function hourIn(timeZone: string, at: Date): number | null {
  try {
    const parts = new Intl.DateTimeFormat("en-GB", {
      timeZone,
      hour: "numeric",
      hour12: false,
    }).formatToParts(at);
    const hour = parts.find((p) => p.type === "hour")?.value;
    return hour === undefined ? null : Number.parseInt(hour, 10);
  } catch {
    // An unknown zone means a bad setting, not a reason to break the page.
    return null;
  }
}

export function initClocks(root: ParentNode, clock: ServerClock, locale: string): void {
  const clocks = root.querySelectorAll<HTMLElement>("[data-clock]");
  if (clocks.length === 0) {
    return;
  }

  const formatters = new Map<string, Intl.DateTimeFormat>();
  const dateFormatters = new Map<string, Intl.DateTimeFormat>();

  const formatterFor = (
    cache: Map<string, Intl.DateTimeFormat>,
    timeZone: string,
    options: Intl.DateTimeFormatOptions,
  ): Intl.DateTimeFormat | null => {
    const cached = cache.get(timeZone);
    if (cached) {
      return cached;
    }
    try {
      const fmt = new Intl.DateTimeFormat(locale, { ...options, timeZone });
      cache.set(timeZone, fmt);
      return fmt;
    } catch {
      return null;
    }
  };

  const tick = (): void => {
    const at = new Date(clock.now());

    for (const el of clocks) {
      const timeZone = el.dataset["clock"];
      if (!timeZone) {
        continue;
      }

      const timeEl = el.querySelector<HTMLElement>("[data-clock-time]");
      if (timeEl) {
        const fmt = formatterFor(formatters, timeZone, {
          hour: "2-digit",
          minute: "2-digit",
        });
        if (fmt) {
          timeEl.textContent = fmt.format(at);
        }
      }

      const dateEl = el.querySelector<HTMLElement>("[data-clock-date]");
      if (dateEl) {
        const fmt = formatterFor(dateFormatters, timeZone, {
          weekday: "short",
          day: "numeric",
          month: "short",
        });
        if (fmt) {
          dateEl.textContent = fmt.format(at);
        }
      }

      const hour = hourIn(timeZone, at);
      if (hour !== null) {
        const daytime = hour >= DAY_START_HOUR && hour < DAY_END_HOUR;
        el.classList.toggle("is-day", daytime);
        el.classList.toggle("is-night", !daytime);
      }
    }
  };

  tick();
  window.setInterval(tick, 1000);
}
