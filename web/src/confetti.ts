/**
 * Confetti for the moment the countdown reaches zero.
 *
 * Purely decorative, so it does nothing at all when the visitor has asked for
 * reduced motion, and it removes itself once the last piece has fallen rather than
 * leaving a canvas animating forever on a phone in someone's pocket.
 */

interface Piece {
  x: number;
  y: number;
  vx: number;
  vy: number;
  size: number;
  rotation: number;
  spin: number;
  colour: string;
}

const PIECE_COUNT = 140;
const GRAVITY = 0.12;
const DRAG = 0.995;
/** How long the animation runs before cleaning itself up. */
const DURATION_MS = 6000;

function prefersReducedMotion(): boolean {
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

/** Reads the deployment's accent colour so the confetti matches the site. */
function palette(): string[] {
  const accent = getComputedStyle(document.documentElement).getPropertyValue("--accent").trim();
  return [accent || "#e5687f", "#ffd166", "#8ecae6", "#ffffff"];
}

function createPiece(width: number, colours: string[]): Piece {
  const colour = colours[Math.floor(Math.random() * colours.length)] ?? "#e5687f";
  return {
    x: Math.random() * width,
    // Start above the fold so pieces drift into view rather than popping in.
    y: -20 - Math.random() * 200,
    vx: (Math.random() - 0.5) * 2.4,
    vy: 1 + Math.random() * 2.5,
    size: 5 + Math.random() * 7,
    rotation: Math.random() * Math.PI * 2,
    spin: (Math.random() - 0.5) * 0.2,
    colour,
  };
}

export function celebrate(): void {
  if (prefersReducedMotion()) {
    return;
  }

  const canvas = document.createElement("canvas");
  canvas.className = "confetti";
  // Decoration only: keep it out of the accessibility tree and out of the way of
  // every click.
  canvas.setAttribute("aria-hidden", "true");
  document.body.append(canvas);

  const context = canvas.getContext("2d");
  if (!context) {
    canvas.remove();
    return;
  }

  const ratio = window.devicePixelRatio || 1;
  const resize = (): void => {
    canvas.width = window.innerWidth * ratio;
    canvas.height = window.innerHeight * ratio;
    context.setTransform(ratio, 0, 0, ratio, 0, 0);
  };
  resize();
  window.addEventListener("resize", resize);

  const colours = palette();
  const pieces = Array.from({ length: PIECE_COUNT }, () => createPiece(window.innerWidth, colours));
  const started = performance.now();

  const frame = (nowMs: number): void => {
    const height = window.innerHeight;
    context.clearRect(0, 0, canvas.width, canvas.height);

    for (const piece of pieces) {
      piece.vy = piece.vy * DRAG + GRAVITY;
      piece.vx *= DRAG;
      piece.x += piece.vx;
      piece.y += piece.vy;
      piece.rotation += piece.spin;

      context.save();
      context.translate(piece.x, piece.y);
      context.rotate(piece.rotation);
      context.fillStyle = piece.colour;
      context.fillRect(-piece.size / 2, -piece.size / 2, piece.size, piece.size * 0.6);
      context.restore();
    }

    const elapsed = nowMs - started;
    const allGone = pieces.every((piece) => piece.y > height + 40);
    if (elapsed > DURATION_MS || allGone) {
      window.removeEventListener("resize", resize);
      canvas.remove();
      return;
    }
    requestAnimationFrame(frame);
  };

  requestAnimationFrame(frame);
}

/**
 * Fires once when the countdown hits zero.
 *
 * The countdown widget raises wfm:reached, and it guards against repeating, so
 * this listener does not have to.
 */
export function initCelebration(root: ParentNode): void {
  root.addEventListener("wfm:reached", () => {
    celebrate();
  });
}
