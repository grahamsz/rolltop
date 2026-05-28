// File overview: Shared React runtime access for browser-loaded frontend plugins.

import type * as ReactTypes from "react";
import type * as ReactDOMTypes from "react-dom";

export type RolltopPluginReactRuntime = {
  React: typeof ReactTypes;
  ReactDOM: Pick<typeof ReactDOMTypes, "createPortal">;
};

type RuntimeGlobal = typeof globalThis & {
  __rolltopPluginReactRuntime?: RolltopPluginReactRuntime;
};

export function installRolltopPluginReactRuntime(runtime: RolltopPluginReactRuntime) {
  (globalThis as RuntimeGlobal).__rolltopPluginReactRuntime = runtime;
}

export function rolltopPluginReactRuntime(): RolltopPluginReactRuntime {
  const runtime = (globalThis as RuntimeGlobal).__rolltopPluginReactRuntime;
  if (!runtime) {
    throw new Error("Rolltop plugin React runtime is not available.");
  }
  return runtime;
}
