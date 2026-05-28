// File overview: JSX runtime shim for runtime plugin bundles.

import type { Key } from "react";
import { rolltopPluginReactRuntime } from "./reactRuntime";

const React = rolltopPluginReactRuntime().React;

export const Fragment = React.Fragment;

export function jsx(type: unknown, props: Record<string, unknown> | null, key?: Key) {
  const nextProps = key === undefined ? props : { ...(props || {}), key };
  return React.createElement(type as never, nextProps);
}

export const jsxs = jsx;
export const jsxDEV = jsx;
