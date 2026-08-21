import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const preview = readFileSync(new URL('../components/files/ArtifactPreview.tsx', import.meta.url), 'utf8')
const browser = readFileSync(new URL('../components/files/FileBrowser.tsx', import.meta.url), 'utf8')
const library = readFileSync(new URL('../components/files/ArtifactLibrary.tsx', import.meta.url), 'utf8')
const editor = readFileSync(new URL('../components/files/FileEditor.tsx', import.meta.url), 'utf8')
const inlineCard = readFileSync(new URL('../components/files/InlineArtifactCard.tsx', import.meta.url), 'utf8')
const picker = readFileSync(new URL('../components/files/FilePicker.tsx', import.meta.url), 'utf8')
const contextMenu = readFileSync(new URL('../components/files/FileContextMenu.tsx', import.meta.url), 'utf8')
const chat = readFileSync(new URL('../components/chat/ChatPanel.tsx', import.meta.url), 'utf8')

test('artifact preview owns async results by exact project, session, and path generation', () => {
  assert.match(preview, /const scopeKey = `\$\{projectID\.length\}:\$\{projectID\}\$\{\(sessionID \|\| ''\)\.length\}:\$\{sessionID \|\| ''\}\$\{path\}`/)
  assert.match(preview, /scopeRef\.current\.generation \+= 1[\s\S]*scopeRef\.current\.mounted = true/)
  assert.match(preview, /scopeRef\.current\.generation === generation[\s\S]*scopeRef\.current\.mounted = false/)
  for (const request of [
    'loadRequestRef',
    'versionsRequestRef',
    'snapshotRequestRef',
    'versionActionRequestRef',
    'copyRequestRef',
  ]) {
    assert.match(preview, new RegExp(`${request}\\.current \\+= 1`), `${request} must be invalidated on scope cleanup`)
  }
})

test('artifact content and version lists accept only their latest owned response', () => {
  const loading = preview.slice(preview.indexOf('const refreshVersions'), preview.indexOf('useEffect(() => {\n    latestRef.current'))
  assert.match(loading, /const request = \+\+versionsRequestRef\.current[\s\S]*!ownsScope\(expectedScope\) \|\| versionsRequestRef\.current !== request/)
  assert.match(loading, /if \(quiet && loadInFlightRef\.current\) return false/)
  assert.match(loading, /const request = \+\+loadRequestRef\.current[\s\S]*await ReadSessionArtifactContent[\s\S]*loadRequestRef\.current !== request/)
  assert.match(loading, /finally \{[\s\S]*loadRequestRef\.current === request[\s\S]*loadInFlightRef\.current = false/)
})

test('artifact live refresh is sequential and releases its timer on scope replacement', () => {
  const polling = preview.slice(preview.indexOf('// Live refresh while'), preview.indexOf('const srcDoc = useMemo'))
  assert.match(polling, /const poll = async \(\) => \{[\s\S]*await load\(true, scope\)[\s\S]*setTimeout\(\(\) => \{ void poll\(\) \}, 1500\)/)
  assert.doesNotMatch(polling, /setInterval/)
  assert.match(polling, /return \(\) => \{[\s\S]*stopped = true[\s\S]*clearTimeout/)
})

test('artifact restore revalidates confirmation ownership and excludes stale live reads', () => {
  const restore = preview.slice(preview.indexOf('const restoreVersion'), preview.indexOf('\n  return ('))
  assert.match(restore, /await requestConfirmation[\s\S]*if \(!accepted \|\| !ownsScope\(scope\)\) return/)
  assert.match(restore, /restoreInFlightRef\.current = true[\s\S]*loadRequestRef\.current \+= 1[\s\S]*await RestoreSessionArtifactVersion/)
  assert.match(restore, /versionActionRequestRef\.current !== request/)
  assert.match(restore, /finally \{[\s\S]*restoreInFlightRef\.current = false[\s\S]*setVersionBusy\(null\)/)
})

test('file workspace remounts editors and previews by exact selected-file identity', () => {
  assert.match(browser, /<ArtifactPreview[\s\S]*?key=\{`\$\{activeProjectId\}:\$\{sessionID\}:\$\{selectedArtifact\}`\}/)
  assert.match(browser, /<FileEditor[\s\S]*?key=\{`\$\{activeProjectId\}:\$\{sessionID\}:\$\{selectedFilePath\}`\}/)
  assert.match(library, /<ArtifactPreview[\s\S]*?key=\{`\$\{activeProjectId\}:\$\{sessionID\}:\$\{selectedArtifact\}`\}/)
})

test('directory expansion and retry ignore superseded responses', () => {
  const directory = browser.slice(browser.indexOf('function DirectoryNode'), browser.indexOf('const extColors'))
  assert.ok([...directory.matchAll(/const request = \+\+requestRef\.current/g)].length >= 2)
  assert.ok([...directory.matchAll(/requestRef\.current !== request/g)].length >= 4)
  assert.match(directory, /useEffect\(\(\) => \(\) => \{ requestRef\.current \+= 1 \}, \[\]\)/)
})

test('file editor save and copy callbacks belong to the exact mounted file generation', () => {
  assert.match(editor, /const saveRequestRef = useRef\(0\)/)
  assert.match(editor, /const copyRequestRef = useRef\(0\)/)
  assert.match(editor, /scopeRef\.current\.generation === generation[\s\S]*saveRequestRef\.current \+= 1[\s\S]*copyRequestRef\.current \+= 1/)

  const save = editor.slice(editor.indexOf('const save = useCallback'), editor.indexOf('useEffect(() => {\n    if (!active)'))
  assert.match(save, /const scope = scopeRef\.current\.generation[\s\S]*const request = \+\+saveRequestRef\.current/)
  assert.match(save, /SaveSessionFileContent\(savedProjectID, savedSessionID, savedPath, content, expectedRevision\)/)
  assert.match(save, /!ownsScope\(scope\) \|\| saveRequestRef\.current !== request/)
  assert.match(save, /finally \{[\s\S]*setSaving\(false\)[\s\S]*onSavingChange\?\.\(false\)/)

  const copy = editor.slice(editor.indexOf('const copyPath = async'), editor.indexOf('const onEditorKeyDown'))
  assert.match(copy, /const request = \+\+copyRequestRef\.current[\s\S]*copyRequestRef\.current !== request/)
  assert.match(copy, /clearTimeout\(copyTimerRef\.current\)[\s\S]*setTimeout/)
})

test('a late save compare-and-deletes only its own cached draft and emits its captured path', () => {
  const save = editor.slice(editor.indexOf('const save = useCallback'), editor.indexOf('useEffect(() => {\n    if (!active)'))
  assert.match(save, /cached\?\.snapshot\.revision === sourceRevision && cached\.draft === content/)
  assert.match(save, /detail: \{ projectID: savedProjectID, sessionID: savedSessionID, path: savedPath \}/)
  assert.ok(save.indexOf('sessionFileDraftCache.delete(savedDraftKey)') < save.indexOf('if (!ownsScope(scope) || saveRequestRef.current !== request) return'))
})

test('controlled artifact navigation uses the same dirty/save gate as local file clicks', () => {
  assert.match(browser, /const \[controlledArtifactPath, setControlledArtifactPath\]/)
  assert.match(browser, /if \(fileSaving\) return false[\s\S]*if \(!fileDirty\) return true/)
  const sync = browser.slice(browser.indexOf('// Top-level Files receives'), browser.indexOf("useEffect(() => {\n    const handler"))
  assert.match(sync, /artifactPath === controlledArtifactPath/)
  assert.match(sync, /openOfficeArtifact\(requested\)\.then\(\(accepted\)/)
  assert.match(sync, /\|\| accepted\) return[\s\S]*onArtifactPathChangeRef\.current\?\.\(previous\)/)
  assert.doesNotMatch(sync, /onArtifactPathChange, openOfficeArtifact/)
  assert.match(browser, /onSavingChange=\{setFileSaving\}/)
  assert.match(editor, /disabled=\{saving\}[\s\S]*Wait for the current save to finish/)
})

test('inline artifact cards accept only the latest response from their exact identity', () => {
  assert.match(inlineCard, /const scopeKey = `\$\{projectID\.length\}:\$\{projectID\}\$\{sessionID\.length\}:\$\{sessionID\}\$\{path\}`/)
  assert.match(inlineCard, /scopeRef\.current\.generation === generation[\s\S]*scopeRef\.current\.mounted = false[\s\S]*loadRequestRef\.current \+= 1/)
  const load = inlineCard.slice(inlineCard.indexOf('const load = useCallback'), inlineCard.indexOf("useEffect(() => {\n    if (!expanded)"))
  assert.match(load, /if \(quiet && loadInFlightRef\.current\) return false/)
  assert.match(load, /const request = \+\+loadRequestRef\.current[\s\S]*await ReadSessionArtifactContent\(projectID, sessionID, path\)/)
  assert.match(load, /!ownsScope\(expectedScope\) \|\| loadRequestRef\.current !== request/)
  assert.match(load, /finally \{[\s\S]*loadRequestRef\.current === request[\s\S]*loadInFlightRef\.current = false/)
  assert.match(chat, /<InlineArtifactCard[\s\S]*key=\{`\$\{projectID\.length\}:\$\{projectID\}\$\{sessionID\.length\}:\$\{sessionID\}\$\{path\}`\}/)
})

test('expanded inline artifact refresh is sequential and stops on collapse or scope replacement', () => {
  const polling = inlineCard.slice(inlineCard.indexOf("useEffect(() => {\n    if (!expanded)"), inlineCard.indexOf('\n  const toggle'))
  assert.match(polling, /const poll = async \(\) => \{[\s\S]*await load\(true, scope\)[\s\S]*setTimeout\(\(\) => \{ void poll\(\) \}, 2500\)/)
  assert.doesNotMatch(polling, /setInterval/)
  assert.match(polling, /return \(\) => \{[\s\S]*stopped = true[\s\S]*clearTimeout/)
  assert.match(inlineCard, /if \(next\) void load\(Boolean\(artifact\)\)/)
})

test('file picker owns directory responses per path and exact project-session scope', () => {
  assert.match(picker, /const directoryRequestsRef = useRef\(new Map<string, number>\(\)\)/)
  assert.match(picker, /const scopeKey = `\$\{projectId\.length\}:\$\{projectId\}\$\{sessionId\.length\}:\$\{sessionId\}`/)
  const load = picker.slice(picker.indexOf('const loadDirectory'), picker.indexOf('\n  useEffect(() => {'))
  assert.match(load, /directoryRequestsRef\.current\.set\(path, request\)/)
  assert.ok([...load.matchAll(/directoryRequestsRef\.current\.get\(path\) !== request/g)].length >= 2)
  assert.ok([...load.matchAll(/!ownsScope\(expectedScope\)/g)].length >= 2)
  assert.match(picker, /directoryRequestsRef\.current\.clear\(\)[\s\S]*void loadDirectory\('', generation\)/)
  assert.match(picker, /requestSequenceRef\.current \+= 1[\s\S]*directoryRequestsRef\.current\.clear\(\)/)
  assert.match(chat, /<FilePicker[\s\S]*key=\{`\$\{activeProjectId\.length\}:\$\{activeProjectId\}\$\{currentSessionId\.length\}:\$\{currentSessionId\}`\}/)
})

test('file context menu invalidates old work synchronously when a new request opens', () => {
  const open = contextMenu.slice(contextMenu.indexOf("const open = (event: Event)"), contextMenu.indexOf("window.addEventListener('gokin:file-context-menu'"))
  assert.match(open, /const generation = \+\+requestSequenceRef\.current[\s\S]*setRequest\(\{[\s\S]*generation,/)
  assert.match(contextMenu, /removeEventListener\('gokin:file-context-menu', open\)[\s\S]*requestSequenceRef\.current \+= 1/)

  const resolve = contextMenu.slice(contextMenu.indexOf("useEffect(() => {\n    if (!request) return"), contextMenu.indexOf("useEffect(() => {\n    if (!request || request.projectID"))
  assert.ok([...resolve.matchAll(/ownsRequest\(generation, projectID\)/g)].length >= 4)
  assert.match(resolve, /requestAnimationFrame\(\(\) => \{[\s\S]*ownsRequest\(generation, projectID\)[\s\S]*button\[role="menuitem"\]:not/)
})

test('file context menu action completion can mutate only its originating request', () => {
  const close = contextMenu.slice(contextMenu.indexOf('const close ='), contextMenu.indexOf('\n  useEffect(() => {'))
  assert.match(close, /expectedGeneration !== undefined && requestSequenceRef\.current !== expectedGeneration/)
  assert.match(close, /const closedGeneration = \+\+requestSequenceRef\.current[\s\S]*requestAnimationFrame[\s\S]*requestSequenceRef\.current === closedGeneration/)

  const run = contextMenu.slice(contextMenu.indexOf('const run = async'), contextMenu.indexOf('\n  return ('))
  assert.match(run, /const \{ generation, projectID \} = request[\s\S]*await action\(\)[\s\S]*if \(!ownsRequest\(generation, projectID\)\) return/)
  assert.match(run, /close\(false, generation\)/)
  assert.match(run, /catch \(reason: any\) \{[\s\S]*if \(!ownsRequest\(generation, projectID\)\) return[\s\S]*setError/)
  assert.match(contextMenu, /aria-busy=\{loading \|\| !!busy\}/)
  assert.match(contextMenu, /querySelectorAll<HTMLButtonElement>\('button\[role="menuitem"\]:not\(\[disabled\]\)'\)/)
})
