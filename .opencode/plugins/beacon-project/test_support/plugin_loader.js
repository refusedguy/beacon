const double = new URL("./plugin_double.js", import.meta.url).href

export function resolve(specifier, context, nextResolve) {
  if (specifier === "@opencode/plugin" || specifier === "@opencode/plugin/tui") {
    return { url: double, shortCircuit: true }
  }
  return nextResolve(specifier, context)
}
