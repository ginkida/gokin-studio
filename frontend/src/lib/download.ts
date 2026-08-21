export const BLOB_DOWNLOAD_REVOKE_DELAY_MS = 1000

type DownloadAnchor = Pick<HTMLAnchorElement, 'href' | 'download' | 'click' | 'remove'>

export type BlobDownloadRuntime = {
  createObjectURL(blob: Blob): string
  revokeObjectURL(url: string): void
  createAnchor(): DownloadAnchor
  appendAnchor(anchor: DownloadAnchor): void
  defer(callback: () => void, delayMs: number): void
}

function browserDownloadRuntime(): BlobDownloadRuntime {
  return {
    createObjectURL: (blob) => URL.createObjectURL(blob),
    revokeObjectURL: (url) => URL.revokeObjectURL(url),
    createAnchor: () => document.createElement('a'),
    appendAnchor: (anchor) => document.body.appendChild(anchor as HTMLAnchorElement),
    defer: (callback, delayMs) => { window.setTimeout(callback, delayMs) },
  }
}

/**
 * Starts a browser download without racing WebKit's asynchronous consumption
 * of the object URL. The optional runtime keeps resource ownership testable.
 */
export function downloadBlob(
  blob: Blob,
  filename: string,
  runtime: BlobDownloadRuntime = browserDownloadRuntime(),
): void {
  const url = runtime.createObjectURL(blob)
  let anchor: DownloadAnchor | undefined

  try {
    anchor = runtime.createAnchor()
    anchor.href = url
    anchor.download = filename
    runtime.appendAnchor(anchor)
    anchor.click()
  } finally {
    try {
      anchor?.remove()
    } finally {
      try {
        runtime.defer(() => runtime.revokeObjectURL(url), BLOB_DOWNLOAD_REVOKE_DELAY_MS)
      } catch {
        // A custom WebView may reject timer scheduling while it is shutting
        // down. Revoke immediately in that exceptional path instead of leaking.
        runtime.revokeObjectURL(url)
      }
    }
  }
}
