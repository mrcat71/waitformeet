/**
 * Gallery lightbox.
 *
 * Progressive enhancement: every thumbnail is a real link to the full picture, so
 * the gallery works with this script absent, blocked or still loading. The click is
 * only intercepted once the dialog is known to be usable.
 */

export function initLightbox(root: ParentNode): void {
  const dialog = root.querySelector<HTMLDialogElement>("[data-lightbox-dialog]");
  const image = root.querySelector<HTMLImageElement>("[data-lightbox-image]");
  const caption = root.querySelector<HTMLElement>("[data-lightbox-caption]");
  const grid = root.querySelector<HTMLElement>("[data-lightbox]");

  // Older browsers without <dialog> keep the plain links, which is a perfectly
  // good gallery.
  if (!dialog || !image || !grid || typeof dialog.showModal !== "function") {
    return;
  }

  const items = Array.from(grid.querySelectorAll<HTMLAnchorElement>("[data-lightbox-item]"));
  let current = -1;

  const show = (index: number): void => {
    const item = items[index];
    if (!item) {
      return;
    }
    current = index;
    image.src = item.href;
    image.alt = item.dataset["caption"] ?? "";
    if (caption) {
      caption.textContent = item.dataset["caption"] ?? "";
    }
    if (!dialog.open) {
      dialog.showModal();
    }
  };

  const step = (delta: number): void => {
    if (items.length === 0) {
      return;
    }
    // Wrap around, so the arrow keys never dead-end.
    show((current + delta + items.length) % items.length);
  };

  items.forEach((item, index) => {
    item.addEventListener("click", (event) => {
      // Leave modified clicks alone: someone asking for a new tab should get one.
      if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
        return;
      }
      event.preventDefault();
      show(index);
    });
  });

  root.querySelector<HTMLElement>("[data-lightbox-close]")?.addEventListener("click", () => {
    dialog.close();
  });

  // Clicking the backdrop closes it. The dialog element itself is the backdrop's
  // event target, so a click landing on the dialog but not on its contents counts.
  dialog.addEventListener("click", (event) => {
    if (event.target === dialog) {
      dialog.close();
    }
  });

  dialog.addEventListener("keydown", (event) => {
    if (event.key === "ArrowRight") {
      event.preventDefault();
      step(1);
    } else if (event.key === "ArrowLeft") {
      event.preventDefault();
      step(-1);
    }
  });
}
