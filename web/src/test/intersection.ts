import { vi } from "vitest";

/** An IntersectionObserver the test drives: nothing is visible until show(). */
export interface FakeIntersection {
  /** Reports every observed element as on screen. */
  show: () => void;
  /** Elements observed and not disconnected. */
  observed: () => number;
}

/** jsdom has no IntersectionObserver: this one is stubbed for the current test. */
export function installIntersectionObserver(): FakeIntersection {
  const observers = new Set<FakeObserver>();

  class FakeObserver {
    readonly elements = new Set<Element>();
    readonly callback: IntersectionObserverCallback;
    constructor(callback: IntersectionObserverCallback) {
      this.callback = callback;
    }
    observe(element: Element) {
      this.elements.add(element);
      observers.add(this);
    }
    unobserve(element: Element) {
      this.elements.delete(element);
    }
    disconnect() {
      this.elements.clear();
      observers.delete(this);
    }
    takeRecords() {
      return [];
    }
    fire() {
      const entries = [...this.elements].map(
        (target) => ({ target, isIntersecting: true }) as IntersectionObserverEntry,
      );
      this.callback(entries, this as unknown as IntersectionObserver);
    }
  }

  vi.stubGlobal("IntersectionObserver", FakeObserver);
  return {
    show: () => {
      for (const observer of [...observers]) {
        observer.fire();
      }
    },
    observed: () => [...observers].reduce((n, o) => n + o.elements.size, 0),
  };
}
