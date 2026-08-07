/** Binds the ticking countdown and the separation progress bar to the DOM. */

import { ServerClock, daysBetween, parseInstant, progress, remaining } from "./time.js";

type Unit = "days" | "hours" | "minutes" | "seconds";

const UNITS: readonly Unit[] = ["days", "hours", "minutes", "seconds"];

/**
 * Plural forms for one unit, keyed by CLDR category. The server renders these into
 * a data attribute so the browser can pick the right form with Intl.PluralRules
 * rather than shipping a second copy of the language rules.
 */
type PluralForms = Partial<Record<Intl.LDMLPluralRule, string>>;

function readForms(el: HTMLElement): PluralForms | null {
  const raw = el.dataset["forms"];
  if (!raw) {
    return null;
  }
  try {
    return JSON.parse(raw) as PluralForms;
  } catch {
    // A malformed attribute must not take the whole countdown down with it.
    return null;
  }
}

function pluralize(forms: PluralForms, rules: Intl.PluralRules, n: number): string {
  const category = rules.select(n);
  return forms[category] ?? forms.other ?? "";
}

/** Renders a two-digit unit; days are left unpadded since they can exceed 99. */
function format(unit: Unit, value: number): string {
  return unit === "days" ? String(value) : String(value).padStart(2, "0");
}

export function initCountdown(root: ParentNode, clock: ServerClock, locale: string): void {
  const containers = root.querySelectorAll<HTMLElement>("[data-countdown]");
  if (containers.length === 0) {
    return;
  }

  const rules = new Intl.PluralRules(locale);

  const tick = (): void => {
    const now = clock.now();

    for (const container of containers) {
      const target = parseInstant(container.dataset["target"]);
      if (target === null) {
        continue;
      }

      const left = remaining(target, now);
      container.classList.toggle("is-reached", left.reached);

      for (const unit of UNITS) {
        const valueEl = container.querySelector<HTMLElement>(`[data-unit="${unit}"]`);
        if (valueEl) {
          const next = format(unit, left[unit]);
          // Only touch the DOM when the text actually changes, so the flip
          // animation fires once per second instead of on every frame.
          if (valueEl.textContent !== next) {
            valueEl.textContent = next;
          }
        }

        const labelEl = container.querySelector<HTMLElement>(`[data-unit-label="${unit}"]`);
        const forms = labelEl ? readForms(labelEl) : null;
        if (labelEl && forms) {
          const next = pluralize(forms, rules, left[unit]);
          if (labelEl.textContent !== next) {
            labelEl.textContent = next;
          }
        }
      }

      if (left.reached && !container.dataset["reachedFired"]) {
        container.dataset["reachedFired"] = "1";
        container.dispatchEvent(new CustomEvent("wfm:reached", { bubbles: true }));
      }
    }
  };

  tick();
  window.setInterval(tick, 1000);
}

export function initProgress(root: ParentNode, clock: ServerClock, locale: string): void {
  const containers = root.querySelectorAll<HTMLElement>("[data-progress]");
  if (containers.length === 0) {
    return;
  }

  const percentFormat = new Intl.NumberFormat(locale, {
    style: "percent",
    maximumFractionDigits: 1,
  });
  const numberFormat = new Intl.NumberFormat(locale);
  const apartRules = new Intl.PluralRules(locale);

  const update = (): void => {
    const now = clock.now();

    for (const container of containers) {
      const start = parseInstant(container.dataset["start"]);
      const end = parseInstant(container.dataset["end"]);
      if (start === null || end === null) {
        continue;
      }

      const fraction = progress(start, end, now);
      const bar = container.querySelector<HTMLElement>("[data-progress-bar]");
      if (bar) {
        bar.style.setProperty("--progress", String(fraction));
      }

      const meter = container.querySelector<HTMLElement>("[role='progressbar']");
      if (meter) {
        meter.setAttribute("aria-valuenow", String(Math.round(fraction * 100)));
      }

      const percentEl = container.querySelector<HTMLElement>("[data-progress-percent]");
      if (percentEl) {
        percentEl.textContent = percentFormat.format(fraction);
      }

      // "Days apart" is a whole sentence rather than a number with a label, because
      // languages put the number in different places: "5 days apart" but "分开 5 天".
      // So the server ships every plural form with a {count} placeholder still in it
      // and the browser fills that in.
      const apartEl = container.querySelector<HTMLElement>("[data-apart]");
      const apartForms = apartEl ? readForms(apartEl) : null;
      if (apartEl && apartForms) {
        const days = daysBetween(start, now);
        const template = pluralize(apartForms, apartRules, days);
        const next = template.replace("{count}", numberFormat.format(days));
        if (apartEl.textContent !== next) {
          apartEl.textContent = next;
        }
      }
    }
  };

  update();
  // A minute is plenty: this shows days and a percentage, neither of which moves
  // fast enough to justify waking the page every second.
  window.setInterval(update, 60_000);
}
