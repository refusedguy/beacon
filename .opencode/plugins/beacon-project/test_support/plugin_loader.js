const double = new URL("./plugin_double.js", import.meta.url).href

// ui_runtime.js imports these peers statically. Plain Node tests have no host
// runtime, so resolve the three specifiers to small in-memory doubles.
const virtual = (source) => `data:text/javascript,${encodeURIComponent(source)}`
const peerDoubles = {
  "@opentui/solid": virtual(`
    export const createElement = (tag) => ({ tag, props: {}, children: [] })
    export const insert = (parent, child) => { parent.children.push(child) }
    export const setProp = (node, name, value) => { node.props[name] = value }
  `),
  "solid-js": virtual(`
    export const createSignal = (initial) => [() => initial, () => {}]
  `),
  "@opentui/core": virtual(`
    export const TextAttributes = { BOLD: 1 }
  `),
}

export function resolve(specifier, context, nextResolve) {
  if (specifier === "@opencode/plugin" || specifier === "@opencode/plugin/tui") {
    return { url: double, shortCircuit: true }
  }
  if (Object.hasOwn(peerDoubles, specifier)) {
    return { url: peerDoubles[specifier], shortCircuit: true }
  }
  return nextResolve(specifier, context)
}
