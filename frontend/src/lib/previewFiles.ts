const STATIC_PREVIEW_RE = /\.(?:html?|pdf|svg|png|jpe?g|gif|webp|avif|bmp|tiff?|heic|heif|ico|mp4|webm|ogv|mov|m4v)$/i
const OFFICE_ARTIFACT_RE = /\.(?:docx|xlsx|pptx)$/i
const INLINE_ARTIFACT_RE = /\.(?:html?|svg|docx|xlsx|pptx|pdf)$/i

export function isStaticPreviewFilePath(path: string): boolean {
  return STATIC_PREVIEW_RE.test(path)
}

export function isOfficeArtifactPath(path: string): boolean {
  return OFFICE_ARTIFACT_RE.test(path)
}

export function isPreviewableFilePath(path: string): boolean {
  return isStaticPreviewFilePath(path) || isOfficeArtifactPath(path)
}

export function isInlineArtifactPath(path: string): boolean {
  return INLINE_ARTIFACT_RE.test(path)
}

export function normalizeMarkdownPreviewPath(value: string): string | null {
  const path = normalizeMarkdownProjectPath(value)
  return path && isPreviewableFilePath(path) ? path : null
}

// A trailing slash is an unambiguous directory signal in prose/code spans.
// Keep it separate from file detection so ordinary identifiers such as `src`
// do not become clickable merely because a project may contain that folder.
export function normalizeMarkdownDirectoryPath(value: string): string | null {
  const trimmed = value.trim().replace(/\\/g, '/')
  if (!trimmed.endsWith('/') || trimmed.length > 4096 || /[\u0000-\u001f\u007f]/.test(trimmed)) return null
  const path = trimmed.replace(/^\.\//, '').replace(/\/+$/, '')
  if (!path || /^(?:[a-z]+:|\/)/i.test(path)) return null
  const parts = path.split('/')
  if (parts.some((part) => !part || part === '..' || part.toLowerCase() === '.git' || part.toLowerCase() === '.gokin')) return null
  return path
}

const EXTENSIONLESS_PROJECT_FILES = new Set([
  'dockerfile', 'makefile', 'rakefile', 'gemfile', 'procfile', 'license', 'readme', 'changelog', 'authors', 'copying',
])

// Markdown code spans contain both source symbols and paths. Keep this
// deliberately conservative: a candidate must be relative, bounded, avoid
// service metadata, and look like a filename/path rather than an expression.
export function normalizeMarkdownProjectPath(value: string): string | null {
  const trimmed = value.trim().replace(/\\/g, '/').replace(/^\.\//, '')
  if (!trimmed || trimmed.length > 4096 || /[\u0000-\u001f\u007f]/.test(trimmed)) return null
  if (/^(?:[a-z]+:|\/)/i.test(trimmed) || trimmed.endsWith('/')) return null
  const parts = trimmed.split('/')
  if (parts.some((part) => !part || part === '..' || part.toLowerCase() === '.git' || part.toLowerCase() === '.gokin')) return null
  const base = parts[parts.length - 1]
  const hasFileExtension = /\.[A-Za-z][A-Za-z0-9_-]{0,15}$/.test(base)
  const looksLikeNestedExtensionlessFile = parts.length > 1 && /^[A-Za-z0-9_@+.-]+$/.test(base)
  if (!hasFileExtension && !looksLikeNestedExtensionlessFile && !EXTENSIONLESS_PROJECT_FILES.has(base.toLowerCase())) return null
  return trimmed
}
