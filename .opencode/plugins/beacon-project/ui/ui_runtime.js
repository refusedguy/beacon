// Beacon TUI peer runtime. The OpenCode host rewriter replaces literal static
// imports of these specifiers with its own Solid and OpenTUI instances. A
// dynamic or computed specifier is invisible to the rewriter, and installing
// peers locally creates a second renderer that fails with "No renderer found".
import * as opentuiSolid from "@opentui/solid"
import * as solid from "solid-js"
import * as opentuiCore from "@opentui/core"

const peers = {
  "@opentui/solid": opentuiSolid,
  "solid-js": solid,
  "@opentui/core": opentuiCore,
}

// Default dependency for ui.js setup. Tests inject their own `import` function
// through the setup deps seam instead of calling this.
export function runtimeImport(specifier) {
  const peer = peers[specifier]
  if (!peer) throw new Error(`unexpected import: ${specifier}`)
  return peer
}
