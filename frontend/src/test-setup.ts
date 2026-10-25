import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach } from 'vitest';
afterEach(cleanup);
Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener() {},
    removeListener() {},
    addEventListener() {},
    removeEventListener() {},
    dispatchEvent() {
      return false;
    },
  }),
});
if (!globalThis.CSS) Object.defineProperty(globalThis, 'CSS', { value: {} });
if (!CSS.escape) CSS.escape = (value) => value.replace(/[^a-zA-Z0-9_-]/g, '\\$&');
Element.prototype.scrollIntoView = () => {};
window.scrollTo = () => {};
if (!HTMLDialogElement.prototype.showModal) {
  HTMLDialogElement.prototype.showModal = function () {
    this.open = true;
  };
}
