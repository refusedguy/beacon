// T3.176. Граница проекта для локального плагина OpenCode V2 (ADR 0069).
//
// ctx.location описывает экземпляр плагина, а не каждую сессию. directory —
// каталог загрузки: в worktree это каталог worktree. project.canonical —
// канонический корень проекта, который переживает запуск из worktree.
// Единственный источник корня проекта — canonical (решение 2 ADR 0069).

import { resolve } from "node:path"

export function locationInfo(ctx) {
  const location = ctx?.location
  if (!location || typeof location !== "object") return null

  const project =
    location.project && typeof location.project === "object" ? location.project : {}
  const directory =
    typeof location.directory === "string" && location.directory
      ? location.directory
      : null
  const canonical =
    typeof project.canonical === "string" && project.canonical
      ? project.canonical
      : null
  const projectDirectory =
    typeof project.directory === "string" && project.directory
      ? project.directory
      : canonical

  return Object.freeze({
    directory,
    workspaceID: typeof location.workspaceID === "string" ? location.workspaceID : null,
    projectID: typeof project.id === "string" && project.id ? project.id : null,
    projectDirectory,
    canonical,
    // canonical имеет приоритет даже когда плагин грузится из worktree.
    root: canonical ?? projectDirectory ?? directory,
  })
}

export function projectRoot(ctx) {
  return locationInfo(ctx)?.root ?? null
}

// directory и canonical различаются при загрузке из worktree.
export function isWorktree(ctx) {
  const info = locationInfo(ctx)
  if (!info?.canonical || !info.directory) return false
  return resolve(info.directory) !== resolve(info.canonical)
}
