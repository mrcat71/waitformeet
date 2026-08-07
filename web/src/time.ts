/**
 * Time arithmetic shared by the countdown and the progress bar.
 *
 * Everything here is pure and takes explicit millisecond arguments so it can be
 * reasoned about (and tested) without touching the DOM or the real clock.
 */

export interface Remaining {
  /** Milliseconds left. Zero once the target has passed. */
  readonly totalMs: number;
  readonly days: number;
  readonly hours: number;
  readonly minutes: number;
  readonly seconds: number;
  /** True once the target is in the past. */
  readonly reached: boolean;
}

const MS_PER_SECOND = 1000;
const SECONDS_PER_MINUTE = 60;
const SECONDS_PER_HOUR = 3600;
const SECONDS_PER_DAY = 86_400;

/** Splits the gap between now and the target into calendar-ish units. */
export function remaining(targetMs: number, nowMs: number): Remaining {
  const totalMs = Math.max(0, targetMs - nowMs);
  const totalSeconds = Math.floor(totalMs / MS_PER_SECOND);

  return {
    totalMs,
    days: Math.floor(totalSeconds / SECONDS_PER_DAY),
    hours: Math.floor(totalSeconds / SECONDS_PER_HOUR) % 24,
    minutes: Math.floor(totalSeconds / SECONDS_PER_MINUTE) % 60,
    seconds: totalSeconds % SECONDS_PER_MINUTE,
    reached: targetMs <= nowMs,
  };
}

/**
 * Fraction of the way from start to end, clamped to 0..1.
 *
 * Returns 1 for a zero-length or inverted range rather than dividing by zero or
 * going negative, because the only sensible reading of "we have already arrived"
 * is a full bar.
 */
export function progress(startMs: number, endMs: number, nowMs: number): number {
  const span = endMs - startMs;
  if (span <= 0) {
    return 1;
  }
  return Math.min(1, Math.max(0, (nowMs - startMs) / span));
}

/**
 * Whole days between two instants, rounded down.
 *
 * This counts elapsed 24-hour periods, not calendar days: "apart for N days" should
 * not tick over at local midnight in whichever timezone the browser happens to be in.
 */
export function daysBetween(fromMs: number, toMs: number): number {
  return Math.max(0, Math.floor((toMs - fromMs) / (SECONDS_PER_DAY * MS_PER_SECOND)));
}

/**
 * Tracks the difference between the server's clock and this browser's.
 *
 * A phone whose clock is a few minutes out would otherwise show a countdown that
 * disagrees with the one the other person is looking at, which for this site is the
 * whole point. The server stamps its own time into the page; everything downstream
 * works in server time.
 */
export class ServerClock {
  private readonly offsetMs: number;

  constructor(serverNowMs: number, clientNowMs: number = Date.now()) {
    this.offsetMs = Number.isFinite(serverNowMs) ? serverNowMs - clientNowMs : 0;
  }

  /** Current time according to the server. */
  now(): number {
    return Date.now() + this.offsetMs;
  }

  /** How far this browser's clock is from the server's, in milliseconds. */
  get skewMs(): number {
    return this.offsetMs;
  }
}

/** Parses an ISO 8601 timestamp, returning null rather than NaN when it is unusable. */
export function parseInstant(value: string | null | undefined): number | null {
  if (!value) {
    return null;
  }
  const ms = Date.parse(value);
  return Number.isNaN(ms) ? null : ms;
}
