import { vi } from "vitest";

// jsdom has no layout engine. Browser tests exercise positioning and scrolling.
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
};
HTMLElement.prototype.scrollIntoView ??= vi.fn();
HTMLElement.prototype.hasPointerCapture ??= () => false;
