// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { observeEmailFrameSize } from "./emailFrameSize";

let frame: HTMLIFrameElement;
let contentHeight: number;
let width: number;
let resized: () => void;
let disconnect: ReturnType<typeof vi.fn>;

beforeEach(() => {
  vi.useFakeTimers();
  vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => window.setTimeout(() => callback(0), 0));
  vi.spyOn(window, "cancelAnimationFrame").mockImplementation((id) => window.clearTimeout(id));
  disconnect = vi.fn();
  vi.stubGlobal("ResizeObserver", class {
    constructor(callback: () => void) { resized = callback; }
    observe() {}
    disconnect = disconnect;
  });
  frame = document.createElement("iframe");
  document.body.appendChild(frame);
  frame.style.height = "96px";
  width = 320;
  contentHeight = 1058;
  Object.defineProperty(frame, "clientWidth", { get: () => width });
  Object.defineProperty(frame.contentDocument!.body, "scrollHeight", { get: () => contentHeight });
  Object.defineProperty(frame.contentDocument!.documentElement, "scrollHeight", {
    get: () => Math.max(contentHeight, Number.parseInt(frame.style.height))
  });
});

afterEach(() => {
  frame.remove();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("measures an initially collapsed message when its card expands", () => {
  width = 0;
  const update = vi.fn((height: number) => { frame.style.height = `${height}px`; });
  const stop = observeEmailFrameSize(frame, 84, update);
  vi.runOnlyPendingTimers();
  expect(update).not.toHaveBeenCalled();
  width = 320;
  resized();
  vi.runOnlyPendingTimers();
  expect(frame.style.height).toBe("1058px");
  stop();
});

it("does not accumulate blank space and can shrink after the viewport widens", () => {
  const stop = observeEmailFrameSize(frame, 84, (height) => { frame.style.height = `${height}px`; });
  vi.runOnlyPendingTimers();
  for (let i = 0; i < 5; i++) {
    resized();
    vi.runOnlyPendingTimers();
    expect(frame.style.height).toBe("1058px");
  }
  contentHeight = 240;
  width = 900;
  resized();
  vi.runOnlyPendingTimers();
  expect(frame.style.height).toBe("240px");
  stop();
});

it("measures late images and cancels queued work when the frame is replaced", () => {
  const update = vi.fn();
  const stop = observeEmailFrameSize(frame, 84, update);
  vi.runOnlyPendingTimers();
  const img = frame.contentDocument!.createElement("img");
  frame.contentDocument!.body.appendChild(img);
  contentHeight = 1800;
  img.dispatchEvent(new Event("load"));
  vi.runOnlyPendingTimers();
  expect(update).toHaveBeenLastCalledWith(1800);
  resized();
  stop();
  update.mockClear();
  img.dispatchEvent(new Event("error"));
  vi.runOnlyPendingTimers();
  expect(update).not.toHaveBeenCalled();
  expect(disconnect).toHaveBeenCalledOnce();
});
