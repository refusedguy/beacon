// Test-side double for the host Plugin object. Production uses the real
// @opencode/plugin and @opencode/plugin/tui exports so one Solid context is used.
export const Plugin = { define: (definition) => definition }
