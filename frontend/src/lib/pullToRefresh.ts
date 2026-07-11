// File overview: Touch-only pull-to-refresh gesture state for scroll-top list surfaces.

import { useEffect, useRef, useState } from "react";

const refreshThreshold = 56;
const maximumPullDistance = 76;

function pageScrollTop() {
  return document.scrollingElement?.scrollTop ?? window.scrollY;
}

function displayedPullDistance(rawDistance: number) {
  return Math.min(maximumPullDistance, Math.max(0, rawDistance) * 0.65);
}

/**
 * usePullToRefresh recognizes a downward, primarily vertical touch beginning at
 * the top of the page. Native touch listeners are used so the active gesture can
 * suppress WebView overscroll while ordinary vertical scrolling remains native.
 */
export function usePullToRefresh<T extends HTMLElement>({
  disabled,
  onRefresh
}: {
  disabled: boolean;
  onRefresh: () => void | Promise<void>;
}) {
  const targetRef = useRef<T>(null);
  const onRefreshRef = useRef(onRefresh);
  const disabledRef = useRef(disabled);
  const distanceRef = useRef(0);
  const [distance, setDistance] = useState(0);

  onRefreshRef.current = onRefresh;
  disabledRef.current = disabled;

  useEffect(() => {
    const target = targetRef.current;
    if (!target) return;

    let tracking = false;
    let startX = 0;
    let startY = 0;

    const updateDistance = (next: number) => {
      distanceRef.current = next;
      setDistance(next);
    };
    const reset = () => {
      tracking = false;
      updateDistance(0);
    };
    const start = (event: TouchEvent) => {
      if (disabledRef.current || event.touches.length !== 1 || pageScrollTop() > 1) return;
      const touch = event.touches[0];
      tracking = true;
      startX = touch.clientX;
      startY = touch.clientY;
    };
    const move = (event: TouchEvent) => {
      if (!tracking || event.touches.length !== 1) return;
      const touch = event.touches[0];
      const rawDistance = touch.clientY - startY;
      const horizontalDistance = Math.abs(touch.clientX - startX);
      if (rawDistance <= 0 || pageScrollTop() > 1) {
        reset();
        return;
      }
      if (horizontalDistance > rawDistance && horizontalDistance > 10) {
        reset();
        return;
      }
      if (rawDistance < 4) return;
      event.preventDefault();
      updateDistance(displayedPullDistance(rawDistance));
    };
    const finish = () => {
      const shouldRefresh = tracking && !disabledRef.current && distanceRef.current >= refreshThreshold;
      reset();
      if (shouldRefresh) void onRefreshRef.current();
    };

    target.addEventListener("touchstart", start, { passive: true });
    target.addEventListener("touchmove", move, { passive: false });
    target.addEventListener("touchend", finish, { passive: true });
    target.addEventListener("touchcancel", reset, { passive: true });
    return () => {
      target.removeEventListener("touchstart", start);
      target.removeEventListener("touchmove", move);
      target.removeEventListener("touchend", finish);
      target.removeEventListener("touchcancel", reset);
    };
  }, []);

  useEffect(() => {
    if (disabled && distanceRef.current > 0) {
      distanceRef.current = 0;
      setDistance(0);
    }
  }, [disabled]);

  return {
    targetRef,
    distance,
    ready: distance >= refreshThreshold
  };
}
