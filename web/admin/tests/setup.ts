import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// Node 25+ ships its own global localStorage/sessionStorage, which shadow jsdom's and are
// unusable without --localstorage-file. Give tests a working in-memory Storage instead.
class MemoryStorage implements Storage {
  private data = new Map<string, string>();
  get length(): number {
    return this.data.size;
  }
  clear(): void {
    this.data.clear();
  }
  getItem(key: string): string | null {
    return this.data.has(key) ? (this.data.get(key) as string) : null;
  }
  key(index: number): string | null {
    return [...this.data.keys()][index] ?? null;
  }
  removeItem(key: string): void {
    this.data.delete(key);
  }
  setItem(key: string, value: string): void {
    this.data.set(key, String(value));
  }
}

function usable(name: "localStorage" | "sessionStorage"): boolean {
  try {
    const s = (globalThis as Record<string, unknown>)[name] as Storage | undefined;
    return typeof s?.clear === "function" && typeof s?.getItem === "function";
  } catch {
    return false;
  }
}

for (const name of ["localStorage", "sessionStorage"] as const) {
  if (!usable(name)) {
    Object.defineProperty(globalThis, name, { value: new MemoryStorage(), configurable: true, writable: true });
  }
}

afterEach(() => {
  cleanup();
});
