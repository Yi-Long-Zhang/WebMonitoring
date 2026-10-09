// jsdom implements the DOM but not the observation and media APIs the console's
// components reach for. Element Plus' table, select and dialog all measure
// themselves on mount, so without these a page test fails in the library rather
// than in the component under test. The stubs are deliberately inert: no test
// here asserts on layout, and a fake that reports a size would be a claim about
// rendering the DOM cannot make.
class InertResizeObserver implements ResizeObserver {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}

if (!("ResizeObserver" in globalThis)) {
  globalThis.ResizeObserver = InertResizeObserver as unknown as typeof ResizeObserver;
}

if (!globalThis.matchMedia) {
  globalThis.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  })) as unknown as typeof globalThis.matchMedia;
}

