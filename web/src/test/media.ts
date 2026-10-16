/** A system color scheme that the tests can flip. */
export interface FakeColorScheme {
  setDark: (dark: boolean) => void;
  /** Listeners on the (prefers-color-scheme: dark) query, i.e. who follows the system. */
  listeners: () => number;
}

/** jsdom has no window.matchMedia: this one answers (prefers-color-scheme: dark). */
export function installMatchMedia(dark: boolean): FakeColorScheme {
  let matches = dark;
  const listeners = new Set<() => void>();
  const query = {
    media: "(prefers-color-scheme: dark)",
    get matches() {
      return matches;
    },
    addEventListener: (_type: string, listener: () => void) => listeners.add(listener),
    removeEventListener: (_type: string, listener: () => void) => listeners.delete(listener),
  };
  window.matchMedia = ((media: string) =>
    media === query.media
      ? query
      : { media, matches: false, addEventListener() {}, removeEventListener() {} }) as never;
  return {
    setDark: (next) => {
      matches = next;
      for (const listener of listeners) {
        listener();
      }
    },
    listeners: () => listeners.size,
  };
}
