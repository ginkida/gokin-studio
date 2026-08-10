import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import { Check, Copy, FileDiff, FileText, Loader2, MessageSquare, Monitor, RefreshCw, Trash2, X, XCircle } from 'lucide-react'
import { GetSessionGitReview } from '../../../wailsjs/go/studio/Studio'
import { ClipboardSetText, EventsOn } from '../../../wailsjs/runtime/runtime'
import { composeInChat } from '../../lib/composeInChat'
import { isStaticPreviewFilePath } from '../../lib/previewFiles'
import { requestFileContextMenu } from '../files/FileContextMenu'

export interface GitReviewFile {
  path: string
  previousPath?: string
  status: string
  patch?: string
  insertions?: number
  deletions?: number
  binary?: boolean
  truncated?: boolean
}

interface GitReview {
  isRepo: boolean
  branch?: string
  files?: GitReviewFile[]
  insertions?: number
  deletions?: number
  diff?: string
  truncated?: boolean
  fingerprint?: string
  findings?: CodeReviewFinding[]
  reviewCompleted?: boolean
}

interface CodeReviewFinding {
  id: string
  path: string
  side: 'old' | 'new'
  line: number
  severity: 'critical' | 'high' | 'medium' | 'low'
  title: string
  body: string
}

interface ReviewLine {
  id: string
  text: string
  kind: 'meta' | 'hunk' | 'context' | 'add' | 'remove'
  oldLine?: number
  newLine?: number
}

interface ReviewComment {
  id: string
  path: string
  side: 'old' | 'new'
  line: number
  text: string
}

interface ActiveComment {
  key: string
  path: string
  side: 'old' | 'new'
  line: number
}

function statusLabel(status: string) {
  switch (status) {
    case 'modified': return 'M'
    case 'added': return 'A'
    case 'deleted': return 'D'
    case 'renamed': return 'R'
    case 'copied': return 'C'
    case 'untracked': return 'U'
    default: return '·'
  }
}

function parsePatch(patch: string): ReviewLine[] {
  let oldLine = 0
  let newLine = 0
  return patch.split('\n').map((text, index) => {
    const hunk = text.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/)
    if (hunk) {
      oldLine = Number(hunk[1])
      newLine = Number(hunk[2])
      return { id: `hunk:${index}`, text, kind: 'hunk' as const }
    }
    if (text.startsWith('diff --git ') || text.startsWith('index ') || text.startsWith('new file mode ') ||
      text.startsWith('deleted file mode ') || text.startsWith('similarity index ') || text.startsWith('rename ') ||
      text.startsWith('--- ') || text.startsWith('+++ ') || text.startsWith('Binary files ') || text.startsWith('GIT binary patch') ||
      text.startsWith('\\ No newline') || text === '[preview truncated]') {
      return { id: `meta:${index}`, text, kind: 'meta' as const }
    }
    if (text.startsWith('+')) {
      const line = newLine++
      return { id: `new:${line}:${index}`, text, kind: 'add' as const, newLine: line }
    }
    if (text.startsWith('-')) {
      const line = oldLine++
      return { id: `old:${line}:${index}`, text, kind: 'remove' as const, oldLine: line }
    }
    if (text.startsWith(' ')) {
      const previous = oldLine++
      const current = newLine++
      return { id: `both:${previous}:${current}:${index}`, text, kind: 'context' as const, oldLine: previous, newLine: current }
    }
    return { id: `meta:${index}`, text, kind: 'meta' as const }
  })
}

function commentTarget(file: GitReviewFile, line: ReviewLine): ActiveComment | null {
  if (line.newLine !== undefined) {
    return { key: `${file.path}:new:${line.newLine}`, path: file.path, side: 'new', line: line.newLine }
  }
  if (line.oldLine !== undefined) {
    return { key: `${file.path}:old:${line.oldLine}`, path: file.path, side: 'old', line: line.oldLine }
  }
  return null
}

function feedbackPrompt(comments: ReviewComment[]) {
  const items = comments.map((comment, index) => (
    `${index + 1}. \`${comment.path}\` (${comment.side === 'new' ? 'new' : 'old'} line ${comment.line}): ${comment.text}`
  )).join('\n')
  return `Please address these inline review comments on the current working-tree changes:\n\n${items}\n\nRe-read the current files before editing in case the diff changed. Keep unrelated changes intact, implement the requested fixes, and verify them.`
}

function codeReviewPrompt(sessionId: string, fingerprint: string) {
  return `Perform a focused code review of the current uncommitted diff. Use review_changes and read surrounding code where needed. Report only definite, actionable issues introduced by these changes: compile/runtime failures, security vulnerabilities, logic errors, regressions, or materially missing tests. Do not report style, lint, speculation, or pre-existing problems. Do not modify files.\n\nWhen finished, call submit_code_review exactly once with session_id=${JSON.stringify(sessionId)}, fingerprint=${JSON.stringify(fingerprint)}, and zero to 20 findings. Every finding path/side/line must point to a visible line in that exact diff. Call it with an empty findings array if there are no actionable issues.`
}

export function GitReviewModal({ projectId, sessionId, onClose, onReviewed, embedded = false }: {
  projectId: string
  sessionId: string
  onClose: () => void
  onReviewed?: () => void
  embedded?: boolean
}) {
  const requestRef = useRef(0)
  const modalRef = useRef<HTMLElement | null>(null)
  const reviewRef = useRef<GitReview | null>(null)
  const fileListRef = useRef<HTMLDivElement | null>(null)
  const [review, setReview] = useState<GitReview | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [selectedPath, setSelectedPath] = useState('')
  const [comments, setComments] = useState<ReviewComment[]>([])
  const commentsRef = useRef<ReviewComment[]>([])
  commentsRef.current = comments
  const [activeComment, setActiveComment] = useState<ActiveComment | null>(null)
  const [commentDraft, setCommentDraft] = useState('')
  const [copyStatus, setCopyStatus] = useState<'idle' | 'copied' | 'error'>('idle')
  const [commentsStale, setCommentsStale] = useState(false)

  const load = useCallback(() => {
    const request = ++requestRef.current
    setLoading(true)
    setError(null)
    setCopyStatus('idle')
    GetSessionGitReview(projectId, sessionId)
      .then((raw: any) => {
        if (requestRef.current !== request) return
        const next = (raw || { isRepo: false }) as GitReview
        if (reviewRef.current?.fingerprint && next.fingerprint !== reviewRef.current.fingerprint && commentsRef.current.length > 0) {
          setCommentsStale(true)
        }
        reviewRef.current = next
        setReview(next)
        setSelectedPath((current) => next.files?.some((file) => file.path === current) ? current : (next.files?.[0]?.path || ''))
        onReviewed?.()
      })
      .catch((reason: any) => {
        if (requestRef.current === request) setError(String(reason?.message || reason))
      })
      .finally(() => {
        if (requestRef.current === request) setLoading(false)
      })
  }, [onReviewed, projectId, sessionId])

  useEffect(() => {
    load()
    return () => { requestRef.current++ }
  }, [load])

  useEffect(() => {
    const saved = (event: Event) => {
      const detail = (event as CustomEvent).detail || {}
      if (detail.projectID === projectId && detail.sessionID === sessionId) load()
    }
    window.addEventListener('gokin:session-file-saved', saved)
    return () => window.removeEventListener('gokin:session-file-saved', saved)
  }, [load, projectId, sessionId])

  useEffect(() => EventsOn('code-review:ready', (detail: any) => {
    if (detail?.projectID === projectId && detail?.sessionID === sessionId) load()
  }), [load, projectId, sessionId])

  const files = review?.files || []
  const codeFindings = review?.findings || []
  const selected = files.find((file) => file.path === selectedPath) || files[0]
  const lines = useMemo(() => parsePatch(selected?.patch || ''), [selected?.patch])

  useEffect(() => {
    if (comments.length === 0) setCommentsStale(false)
  }, [comments.length])

  const moveFileSelection = (event: ReactKeyboardEvent, currentIndex: number) => {
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key) || files.length === 0) return
    event.preventDefault()
    const nextIndex = event.key === 'Home' ? 0
      : event.key === 'End' ? files.length - 1
        : event.key === 'ArrowDown' ? (currentIndex + 1) % files.length
          : (currentIndex - 1 + files.length) % files.length
    setSelectedPath(files[nextIndex].path)
    setActiveComment(null)
    setCommentDraft('')
    window.requestAnimationFrame(() => {
      fileListRef.current?.querySelectorAll<HTMLButtonElement>('[role="option"]')[nextIndex]?.focus()
    })
  }

  const addComment = useCallback(() => {
    const text = commentDraft.trim()
    if (!activeComment || !text) return
    setComments((current) => [...current, {
      id: `${activeComment.key}:${Date.now()}:${current.length}`,
      path: activeComment.path,
      side: activeComment.side,
      line: activeComment.line,
      text,
    }])
    setCommentDraft('')
    setActiveComment(null)
  }, [activeComment, commentDraft])

  const submitComments = useCallback((pending?: ReviewComment) => {
    const all = pending ? [...comments, pending] : comments
    if (all.length === 0) return
    composeInChat(feedbackPrompt(all), 'replace')
    onClose()
  }, [comments, onClose])

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (embedded && !modalRef.current?.contains(document.activeElement)) return
      if (event.key === 'Escape') {
        event.preventDefault()
        if (activeComment) {
          setActiveComment(null)
          setCommentDraft('')
        } else {
          onClose()
        }
        return
      }
      if (event.key !== 'Enter' || !(event.metaKey || event.ctrlKey)) return
      event.preventDefault()
      const text = commentDraft.trim()
      if (activeComment && text) {
        submitComments({
          id: `${activeComment.key}:${Date.now()}`,
          path: activeComment.path,
          side: activeComment.side,
          line: activeComment.line,
          text,
        })
      } else {
        submitComments()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [activeComment, commentDraft, embedded, onClose, submitComments])

  const copyPatch = async () => {
    try {
      await ClipboardSetText(selected?.patch || review?.diff || '')
      setCopyStatus('copied')
    } catch {
      setCopyStatus('error')
    }
    window.setTimeout(() => setCopyStatus('idle'), 1800)
  }

  return (
    <div className={`git-review-backdrop ${embedded ? 'embedded' : ''}`} onMouseDown={embedded ? undefined : onClose}>
      <section
        ref={modalRef}
        className={`git-review-modal ${embedded ? 'embedded' : ''}`}
        role={embedded ? 'region' : 'dialog'}
        aria-modal={embedded ? undefined : true}
        aria-labelledby="git-review-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="git-review-header">
          <div>
            <FileDiff size={16} />
            <span>
              <strong id="git-review-title">Review changes</strong>
              <small>{review?.branch || 'Working tree'} · <span className="cg-add">+{review?.insertions || 0}</span> <span className="cg-del">-{review?.deletions || 0}</span></small>
            </span>
          </div>
          <div className="git-review-header-actions">
            <button onClick={load} disabled={loading} aria-label="Refresh changes" title="Refresh changes"><RefreshCw size={15} className={loading ? 'spin' : ''} /></button>
            <button onClick={onClose} aria-label="Close Git review" title="Close (Esc)"><X size={16} /></button>
          </div>
        </header>

        {review?.truncated && <div className="git-review-warning">Large review was bounded for responsiveness. Truncated files are marked in the list.</div>}
        {commentsStale && <div className="git-review-warning">The diff changed after comments were added. Recheck their line references before submitting.</div>}

        <div className="git-review-workspace">
          {loading && !review ? (
            <div className="git-review-state"><Loader2 size={18} className="spin" /> Loading changes…</div>
          ) : error ? (
            <div className="git-review-state error"><span>{error}</span><button onClick={load}><RefreshCw size={12} /> Retry</button></div>
          ) : !review?.isRepo ? (
            <div className="git-review-state">This session is not a Git repository.</div>
          ) : files.length === 0 ? (
            <div className="git-review-state">Working tree is clean.</div>
          ) : (
            <>
              <aside className="git-review-files" aria-label="Changed files">
                <div className="git-review-files-heading">{files.length} changed file{files.length === 1 ? '' : 's'}</div>
                <div ref={fileListRef} role="listbox" aria-label="Choose a changed file">
                  {files.map((file, fileIndex) => {
                    const commentCount = comments.filter((comment) => comment.path === file.path).length + codeFindings.filter((finding) => finding.path === file.path).length
                    return (
                      <button
                        key={file.path}
                        type="button"
                        role="option"
                        aria-selected={selected?.path === file.path}
                        className={selected?.path === file.path ? 'active' : ''}
                        title={file.previousPath ? `${file.previousPath} → ${file.path}` : file.path}
                        onClick={() => { setSelectedPath(file.path); setActiveComment(null); setCommentDraft('') }}
                        onContextMenu={(event) => {
                          event.preventDefault()
                          requestFileContextMenu(file.path, sessionId, event.clientX, event.clientY, event.currentTarget)
                        }}
                        onKeyDown={(event) => {
                          if (event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10')) {
                            event.preventDefault()
                            const rect = event.currentTarget.getBoundingClientRect()
                            requestFileContextMenu(file.path, sessionId, rect.left + 18, rect.bottom, event.currentTarget)
                            return
                          }
                          moveFileSelection(event, fileIndex)
                        }}
                      >
                        <span className={`git-review-file-status status-${file.status}`}>{statusLabel(file.status)}</span>
                        <span className="git-review-file-name">{file.path}</span>
                        <span className="git-review-file-stat"><i>+{file.insertions || 0}</i><b>-{file.deletions || 0}</b></span>
                        {commentCount > 0 && <span className="git-review-file-comments" aria-label={`${commentCount} comments`}>{commentCount}</span>}
                        {file.truncated && <span className="git-review-file-truncated" title="Preview truncated">…</span>}
                      </button>
                    )
                  })}
                </div>
              </aside>

              <main className="git-review-file-view">
                <div className="git-review-file-header">
                  <span
                    title={selected?.path}
                    onContextMenu={(event) => {
                      if (!selected) return
                      event.preventDefault()
                      requestFileContextMenu(selected.path, sessionId, event.clientX, event.clientY, event.currentTarget)
                    }}
                  >{selected?.path}</span>
                  {selected?.previousPath && <small>{selected.previousPath} →</small>}
                  {selected?.binary && <em>Binary</em>}
                  {selected && isStaticPreviewFilePath(selected.path) && (
                    <button
                      type="button"
                      onClick={() => window.dispatchEvent(new CustomEvent('gokin:open-artifact', { detail: { path: selected.path, sessionID: sessionId } }))}
                      title={`Open ${selected.path} in Preview`}
                    ><Monitor size={11} /> Preview</button>
                  )}
                  {selected && !selected.binary && selected.status !== 'deleted' && !isStaticPreviewFilePath(selected.path) && (
                    <button
                      type="button"
                      onClick={() => window.dispatchEvent(new CustomEvent('gokin:open-file', { detail: { path: selected.path, sessionID: sessionId } }))}
                      title={`Open ${selected.path} in Files`}
                    ><FileText size={11} /> Open file</button>
                  )}
                </div>
                {selected?.binary || !selected?.patch ? (
                  <div className="git-review-state">{selected?.binary ? 'Binary file changed. A text diff is not available.' : 'No text diff is available for this file.'}</div>
                ) : (
                  <div className="git-review-diff" role="table" aria-label={`Diff for ${selected.path}`}>
                    {lines.map((line) => {
                      const target = commentTarget(selected, line)
                      const lineComments = target ? comments.filter((comment) => (
                        comment.path === target.path && comment.side === target.side && comment.line === target.line
                      )) : []
                      const lineFindings = target ? codeFindings.filter((finding) => (
                        finding.path === target.path && finding.side === target.side && finding.line === target.line
                      )) : []
                      return (
                        <div className="git-review-line-group" key={line.id}>
                          <button
                            type="button"
                            className={`git-review-line ${line.kind}`}
                            disabled={!target}
                            onClick={() => {
                              if (!target) return
                              setActiveComment(target)
                              setCommentDraft('')
                            }}
                            aria-label={target ? `Comment on ${target.side} line ${target.line}: ${line.text}` : line.text}
                          >
                            <span className="git-review-line-number old">{line.oldLine ?? ''}</span>
                            <span className="git-review-line-number new">{line.newLine ?? ''}</span>
                            <code>{line.text || ' '}</code>
                            {lineComments.length + lineFindings.length > 0 && <span className="git-review-line-comment-count">{lineComments.length + lineFindings.length}</span>}
                          </button>
                          {lineFindings.map((finding) => (
                            <div className={`git-review-finding severity-${finding.severity}`} key={finding.id}>
                              <div>
                                <span className="git-review-finding-severity">{finding.severity}</span>
                                <strong>{finding.title}</strong>
                              </div>
                              <p>{finding.body}</p>
                              <button type="button" onClick={() => composeInChat(
                                `Fix this code review finding in the current working tree:\n\n${finding.path} (${finding.side} line ${finding.line}) — ${finding.title}\n${finding.body}\n\nRe-read the current file and diff before editing, preserve unrelated changes, implement the smallest correct fix, and run relevant tests.`,
                                'replace',
                              )}>Ask to fix</button>
                            </div>
                          ))}
                          {lineComments.map((comment) => (
                            <div className="git-review-comment" key={comment.id}>
                              <MessageSquare size={12} />
                              <span>{comment.text}</span>
                              <button type="button" onClick={() => setComments((current) => current.filter((item) => item.id !== comment.id))} aria-label="Delete review comment" title="Delete comment"><Trash2 size={12} /></button>
                            </div>
                          ))}
                          {activeComment?.key === target?.key && (
                            <div className="git-review-comment-editor">
                              <textarea
                                autoFocus
                                value={commentDraft}
                                onChange={(event) => setCommentDraft(event.target.value)}
                                onKeyDown={(event) => {
                                  if (event.nativeEvent.isComposing || event.keyCode === 229) return
                                  if (event.key === 'Enter' && !event.shiftKey && !event.metaKey && !event.ctrlKey) {
                                    event.preventDefault()
                                    addComment()
                                  }
                                }}
                                placeholder={`Comment on ${target?.side || 'changed'} line ${target?.line || ''}…`}
                                aria-label={`Comment on ${target?.side || 'changed'} line ${target?.line || ''}`}
                                rows={2}
                              />
                              <div><span>Enter to add · Shift+Enter for a new line</span><button type="button" onClick={() => { setActiveComment(null); setCommentDraft('') }}>Cancel</button><button type="button" onClick={addComment} disabled={!commentDraft.trim()}>Add comment</button></div>
                            </div>
                          )}
                        </div>
                      )
                    })}
                  </div>
                )}
              </main>
            </>
          )}
        </div>

        <footer className="git-review-footer">
          <span>{comments.length > 0
            ? `${comments.length} inline comment${comments.length === 1 ? '' : 's'} · Cmd/Ctrl+Enter to send`
            : review?.reviewCompleted
              ? codeFindings.length > 0 ? `${codeFindings.length} code review finding${codeFindings.length === 1 ? '' : 's'}` : 'Code review complete · no actionable issues'
              : 'Click a changed line to leave feedback'}</span>
          <button disabled={!selected?.patch && !review?.diff} className={copyStatus === 'error' ? 'is-error' : ''} onClick={copyPatch}>
            {copyStatus === 'copied' ? <Check size={13} /> : copyStatus === 'error' ? <XCircle size={13} /> : <Copy size={13} />}
            {copyStatus === 'copied' ? 'Copied' : copyStatus === 'error' ? 'Copy failed' : 'Copy patch'}
          </button>
          <button
            className="git-review-ask"
            disabled={loading || !!error || !review?.isRepo || files.length === 0}
            onClick={() => {
              if (!review?.fingerprint) return
              window.dispatchEvent(new CustomEvent('gokin:start-code-review', { detail: {
                projectID: projectId,
                sessionID: sessionId,
                fingerprint: review.fingerprint,
                prompt: codeReviewPrompt(sessionId, review.fingerprint),
              }}))
            }}
          >
            <MessageSquare size={13} /> Review code
          </button>
          <button className="git-review-submit" disabled={comments.length === 0} onClick={() => submitComments()}>
            {comments.length > 0 ? `Send ${comments.length} comment${comments.length === 1 ? '' : 's'}` : 'Send comments'}
          </button>
        </footer>
      </section>
    </div>
  )
}
