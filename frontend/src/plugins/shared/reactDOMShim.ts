// File overview: ReactDOM import shim for runtime plugin bundles.

import { rolltopPluginReactRuntime } from "./reactRuntime";

export const createPortal = rolltopPluginReactRuntime().ReactDOM.createPortal;
