import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const preview = readFileSync(new URL('../components/preview/LivePreviewPane.tsx', import.meta.url), 'utf8')

test('preview mutating actions belong to an exact project-session scope', () => {
  assert.match(preview, /const scopeKey = `\$\{projectId\.length\}:\$\{projectId\}\$\{sessionId\.length\}:\$\{sessionId\}`/)
  assert.match(preview, /scopeRef\.current\.generation \+= 1[\s\S]*scopeRef\.current\.mounted = true/)
  const cleanup = preview.slice(preview.indexOf('return () => {\n      if (scopeRef.current.generation'), preview.indexOf('\n  }, [scopeKey]'))
  assert.match(cleanup, /scopeRef\.current\.mounted = false[\s\S]*serverActionRequestRef\.current \+= 1[\s\S]*serverStatusRequestRef\.current \+= 1[\s\S]*persistenceActionRequestRef\.current \+= 1/)
  assert.match(cleanup, /clearTimeout\(previewReloadTimerRef\.current\)/)
  assert.match(preview, /const ownsScope = useCallback[\s\S]*scopeRef\.current\.mounted && scopeRef\.current\.generation === generation/)
})

test('preview server actions lock before confirmation and revalidate captured configuration', () => {
  const begin = preview.slice(preview.indexOf('const beginServerAction'), preview.indexOf('\n  const ownsServerAction'))
  assert.match(begin, /serverActionInFlightRef\.current[\s\S]*persistenceActionInFlightRef\.current[\s\S]*previewCommandInFlightRef\.current[\s\S]*externalNavigationInFlightRef\.current[\s\S]*externalAgentInFlightRef\.current[\s\S]*closingExternalTabIDRef\.current/)
  assert.match(begin, /serverActionInFlightRef\.current = true[\s\S]*request: \+\+serverActionRequestRef\.current[\s\S]*setBusy\(true\)/)
  assert.match(begin, /serverStatusRequestRef\.current \+= 1/)

  const start = preview.slice(preview.indexOf('const start = async'), preview.indexOf('\n  const stop = async'))
  assert.ok(start.indexOf('const owner = beginServerAction()') < start.indexOf('await requestConfirmation'))
  assert.match(start, /const target = \{ \.\.\.selected \}/)
  assert.match(start, /!accepted \|\| !ownsServerAction\(owner\)/)
  assert.match(start, /selectedSnapshotRef\.current\.name !== target\.name \|\| selectedSnapshotRef\.current\.command !== target\.command/)
  assert.match(start, /await StartSessionPreviewServer\(projectId, sessionId, target\.name, target\.command\)[\s\S]*if \(!ownsServerAction\(owner\)\) return[\s\S]*setStatus/)

  const save = preview.slice(preview.indexOf('const saveDetectedConfiguration'), preview.indexOf('\n  const clearFrameStorage'))
  assert.ok(save.indexOf('const owner = beginServerAction()') < save.indexOf('await requestConfirmation'))
  assert.match(save, /await SaveDetectedSessionPreviewConfig\(projectId, sessionId, target\.name, target\.command\)[\s\S]*!ownsServerAction\(owner\)/)
})

test('preview persistence operations own confirmation, RPC, frame clear, and delayed reload', () => {
  const begin = preview.slice(preview.indexOf('const beginPersistenceAction'), preview.indexOf('\n  const ownsPersistenceAction'))
  assert.match(begin, /persistenceActionInFlightRef\.current[\s\S]*serverActionInFlightRef\.current[\s\S]*previewCommandInFlightRef\.current[\s\S]*externalNavigationInFlightRef\.current[\s\S]*externalAgentInFlightRef\.current[\s\S]*closingExternalTabIDRef\.current/)
  assert.match(begin, /persistenceActionInFlightRef\.current = true[\s\S]*request: \+\+persistenceActionRequestRef\.current[\s\S]*setPersistenceBusy\(true\)/)
  assert.match(begin, /persistenceRequestRef\.current \+= 1[\s\S]*clearTimeout\(persistenceRefreshTimerRef\.current\)/)

  const toggle = preview.slice(preview.indexOf('const togglePersistence'), preview.indexOf('\n  const clearPersistence'))
  assert.ok(toggle.indexOf('const owner = beginPersistenceAction()') < toggle.indexOf('await requestConfirmation'))
  assert.match(toggle, /!accepted \|\| !ownsPersistenceAction\(owner\) \|\| selectedSnapshotRef\.current\.name !== targetName/)
  assert.match(toggle, /await SetPreviewSessionPersistence\(projectId, sessionId, targetName, enabling\)[\s\S]*!ownsPersistenceAction\(owner\)/)
  assert.match(toggle, /persistenceRequestRef\.current \+= 1[\s\S]*setPersistence/)
  assert.match(toggle, /clearFrameStorage\(frameTarget, bridgeToken\)[\s\S]*schedulePreviewReload\(owner, targetName\)/)

  const reload = preview.slice(preview.indexOf('const schedulePreviewReload'), preview.indexOf('\n\n  const upsertExternalTab'))
  assert.match(reload, /ownsPersistenceAction\(owner\) && selectedSnapshotRef\.current\.name === configuration/)
  assert.match(reload, /previewReloadTimerRef\.current === timer/)
  assert.doesNotMatch(preview, /setTimeout\(\(\) => setRevision/)
})

test('preview status and persistence reads cannot overwrite a mutation result', () => {
  const persistenceRead = preview.slice(preview.indexOf('const loadPersistence'), preview.indexOf('\n  useEffect(() => {\n    void loadPersistence'))
  assert.match(persistenceRead, /if \(persistenceActionInFlightRef\.current\) return/)
  assert.match(persistenceRead, /const request = \+\+persistenceRequestRef\.current[\s\S]*persistenceRequestRef\.current === request/)

  const polling = preview.slice(preview.indexOf("useEffect(() => {\n    if (staticPath) return"), preview.indexOf('// The Desktop autoVerify contract'))
  assert.match(polling, /serverActionInFlightRef\.current[\s\S]*const request = \+\+serverStatusRequestRef\.current/)
  assert.ok([...polling.matchAll(/serverStatusRequestRef\.current === request/g)].length >= 2)
  assert.match(polling, /return \(\) => \{ stopped = true; serverStatusRequestRef\.current \+= 1; window\.clearInterval/)

  const storageSave = preview.indexOf('SavePreviewBrowserStorage', preview.indexOf('const inspectPreview'))
  const bridge = preview.slice(preview.lastIndexOf('useEffect(() => {', storageSave), preview.indexOf('\n  const startElementSelection =', storageSave))
  assert.match(bridge, /SavePreviewBrowserStorage[\s\S]*\.then\(\(\) => \{[\s\S]*if \(!active\) return/)
  assert.match(bridge, /const timer = window\.setTimeout[\s\S]*persistenceRefreshTimerRef\.current === timer[\s\S]*if \(active\) void loadPersistence\(\)/)
  assert.match(bridge, /return \(\) => \{[\s\S]*active = false[\s\S]*clearTimeout\(persistenceRefreshTimerRef\.current\)[\s\S]*persistenceRefreshTimerRef\.current = null/)
})

test('preview commands acquire one exact scope-frame-request owner before postMessage', () => {
  const owns = preview.slice(preview.indexOf('const ownsPreviewCommand'), preview.indexOf('\n\n  const beginServerAction'))
  assert.match(owns, /previewCommandInFlightRef\.current === owner[\s\S]*ownsScope\(owner\.scope\)/)
  assert.match(owns, /frameRef\.current\?\.contentWindow === owner\.target/)
  assert.match(owns, /previewBridgeIdentityRef\.current\.configuration === owner\.configuration[\s\S]*previewBridgeIdentityRef\.current\.bridgeToken === owner\.bridgeToken/)

  const run = preview.slice(preview.indexOf('const runPreviewCommand'), preview.indexOf('\n\n  const inspectPreview'))
  assert.match(run, /previewCommandInFlightRef\.current[\s\S]*serverActionInFlightRef\.current[\s\S]*persistenceActionInFlightRef\.current[\s\S]*externalNavigationInFlightRef\.current[\s\S]*externalAgentInFlightRef\.current[\s\S]*closingExternalTabIDRef\.current/)
  assert.match(run, /const owner: PreviewCommandOwner = \{[\s\S]*scope: scopeRef\.current\.generation,[\s\S]*requestID: requestId,[\s\S]*configuration,[\s\S]*bridgeToken: token,[\s\S]*action,[\s\S]*target/)
  assert.ok(run.indexOf('previewCommandInFlightRef.current = owner') < run.indexOf('setInspecting(true)'))
  assert.ok(run.indexOf('previewCommandInFlightRef.current = owner') < run.indexOf("target.postMessage({ type: 'gokin-preview-command'"))
  assert.match(run, /await new Promise<PreviewInspection>[\s\S]*if \(!ownsPreviewCommand\(owner\)\) throw new Error/)
  assert.match(run, /if \(!suppliedRequestId && previewCommandInFlightRef\.current === owner\)[\s\S]*previewCommandInFlightRef\.current = null[\s\S]*ownsScope\(owner\.scope\)/)
})

test('preview model requests retain their exact owner through backend resolution', () => {
  const handler = preview.slice(preview.indexOf("useEffect(() => EventsOn('preview:browser_request'"), preview.indexOf("\n\n  useEffect(() => {\n    if (!staticPath) return"))
  assert.match(handler, /request\?\.configuration !== activeConfiguration \|\| request\?\.bridgeToken !== status\.bridgeToken/)
  assert.match(handler, /const inFlight = previewCommandInFlightRef\.current/)
  assert.match(handler, /inFlight\.requestID === requestID && inFlight\.configuration === request\.configuration && inFlight\.bridgeToken === request\.bridgeToken/)
  assert.match(handler, /Another Preview interaction is already in progress/)
  assert.match(handler, /serverActionInFlightRef\.current \|\| persistenceActionInFlightRef\.current/)
  assert.ok(handler.indexOf('const command = runPreviewCommand') < handler.indexOf('const owner = previewCommandInFlightRef.current'))
  assert.match(handler, /\.then\(\(payload\) => ResolvePreviewBrowserRequest[\s\S]*\.catch\(\(reason\) => ResolvePreviewBrowserRequest[\s\S]*\.catch\(\(\) =>/)
  assert.match(handler, /\.finally\(\(\) => \{[\s\S]*owner && previewCommandInFlightRef\.current === owner[\s\S]*previewCommandInFlightRef\.current = null[\s\S]*ownsScope\(owner\.scope\)/)
})

test('element selection starts once and stale completion cannot clear its replacement', () => {
  const selection = preview.slice(preview.indexOf('const startElementSelection'), preview.indexOf('\n  startElementSelectionRef.current'))
  assert.match(selection, /selectingElementRef\.current \|\|[\s\S]*previewCommandInFlightRef\.current \|\|[\s\S]*serverActionInFlightRef\.current \|\|[\s\S]*persistenceActionInFlightRef\.current/)
  assert.match(selection, /const scope = scopeRef\.current\.generation[\s\S]*const request = \+\+elementSelectionRequestRef\.current/)
  assert.match(selection, /ownsScope\(scope\) && elementSelectionRequestRef\.current === request/)
  assert.match(selection, /runPreviewCommand\(\{ action: 'select_element'[\s\S]*if \(!ownsSelection\(\) \|\| result\.cancelled\) return/)
  assert.match(selection, /\.catch[\s\S]*if \(ownsSelection\(\)\) setError[\s\S]*\.finally[\s\S]*if \(!ownsSelection\(\)\) return[\s\S]*setSelectingElement\(false\)/)
  assert.match(selection, /return true/)

  const requested = preview.slice(preview.indexOf("useEffect(() => {\n    if (elementSelectRequest"), preview.indexOf("\n\n  useEffect(() => EventsOn('preview:browser_request'"))
  assert.match(requested, /if \(startElementSelection\(\)\) handledElementSelectRequestRef\.current = elementSelectRequest/)
  assert.match(requested, /\[elementSelectRequest, frameLoaded, inspecting, startElementSelection, status\.bridgeToken, status\.state\]/)
})

test('a replaced external browser tab cannot project a late ready callback into the active address', () => {
  const bridge = preview.slice(preview.indexOf("useEffect(() => {\n    let active = true", preview.indexOf('const closeExternalTab')), preview.indexOf('\n  const runExternalAgentCommand'))
  assert.match(bridge, /UpdateExternalBrowserTabState[\s\S]*\.then\(\(\) => \{[\s\S]*if \(!active\) return[\s\S]*setBrowserAddress/)
  assert.match(bridge, /return \(\) => \{[\s\S]*active = false[\s\S]*removeEventListener\('message', onMessage\)/)
})

test('external browser navigation owns review, approval, and mutation for one exact tab', () => {
  const begin = preview.slice(preview.indexOf('const beginExternalNavigation'), preview.indexOf('\n  const ownsExternalNavigation'))
  assert.match(begin, /externalNavigationInFlightRef\.current[\s\S]*externalNavigationInFlightRef\.current = true/)
  assert.match(begin, /scope: scopeRef\.current\.generation[\s\S]*request: \+\+externalNavigationRequestRef\.current[\s\S]*targetTabID/)

  const owns = preview.slice(preview.indexOf('const ownsExternalNavigation'), preview.indexOf('\n  const finishExternalNavigation'))
  assert.match(owns, /ownsScope\(owner\.scope\)[\s\S]*externalNavigationRequestRef\.current === owner\.request[\s\S]*activeBrowserTabIDRef\.current === owner\.targetTabID/)

  const perform = preview.slice(preview.indexOf('const performExternalNavigation'), preview.indexOf('\n  const commitExternalNavigation'))
  assert.match(perform, /if \(!ownsExternalNavigation\(owner\)\) return false/)
  assert.match(perform, /await NavigateExternalBrowserTab[\s\S]*await OpenExternalBrowserTab[\s\S]*if \(!ownsExternalNavigation\(owner\)\) return false[\s\S]*upsertExternalTab/)

  const request = preview.slice(preview.indexOf('const requestExternalNavigation'), preview.indexOf('\n  const closeExternalTab'))
  assert.match(request, /!trimmed[\s\S]*externalNavigationInFlightRef\.current[\s\S]*externalAgentInFlightRef\.current[\s\S]*previewCommandInFlightRef\.current[\s\S]*serverActionInFlightRef\.current[\s\S]*persistenceActionInFlightRef\.current/)
  assert.match(request, /closingExternalTabIDRef\.current === targetTabID/)
  assert.match(request, /activeBrowserTabIDRef\.current = 'new'[\s\S]*const owner = beginExternalNavigation\(targetTabID\)/)
  assert.match(request, /await ReviewExternalBrowserNavigation\(candidate\)[\s\S]*if \(!ownsExternalNavigation\(owner\)\) return true/)
  assert.match(request, /setPendingExternalApproval\(\{ review, owner, tabID \}\)/)

  const commit = preview.slice(preview.indexOf('const commitExternalNavigation'), preview.indexOf('\n  const requestExternalNavigation'))
  assert.match(commit, /externalNavigationInFlightRef\.current \|\| !ownsExternalNavigation\(pending\.owner\)/)
  assert.match(commit, /performExternalNavigation\(pending\.owner, pending\.review, approval, pending\.tabID\)[\s\S]*finishExternalNavigation\(pending\.owner\)/)
})

test('external tab discovery and active routing reject stale scope results', () => {
  assert.match(preview, /scopedExternalTabs = useMemo[\s\S]*tab\.projectID === projectId && tab\.sessionID === sessionId/)
  const scope = preview.slice(preview.indexOf("useEffect(() => {\n    scopeRef.current.generation"), preview.indexOf('\n  const ownsScope'))
  assert.match(scope, /activeExternalSyncRequestRef\.current \+= 1[\s\S]*activeBrowserTabIDRef\.current = null/)
  assert.match(scope, /setPendingExternalApproval\(null\)/)
  assert.match(scope, /setExternalTabs\(\[\]\)[\s\S]*setActiveBrowserTabID\(null\)/)

  const discovery = preview.slice(preview.indexOf("useEffect(() => {\n    const request = ++externalTabsLoadRequestRef.current"), preview.indexOf("\n  useEffect(() => {\n    const tabID = activeExternalTab"))
  assert.match(discovery, /externalTabsLoadRequestRef\.current !== request/)
  assert.match(discovery, /externalTabsLoadRequestRef\.current === request/)
  assert.match(discovery, /filter\(\(tab\) => tab\.projectID === projectId && tab\.sessionID === sessionId\)/)

  const sync = preview.slice(preview.indexOf("useEffect(() => {\n    const tabID = activeExternalTab"), preview.indexOf('\n\n  useEffect(() => () => {', preview.indexOf('const tabID = activeExternalTab')))
  assert.match(sync, /const request = \+\+activeExternalSyncRequestRef\.current/)
  assert.match(sync, /ownsScope\(scope\)[\s\S]*activeExternalSyncRequestRef\.current === request[\s\S]*activeBrowserTabIDRef\.current/)
  assert.match(sync, /ownsSync\(\) \? SetActiveExternalBrowserTab/)
})

test('busy external link requests retry and the browser UI shares the navigation lock', () => {
  const incoming = preview.slice(preview.indexOf("useEffect(() => {\n    if (!externalNavigationRequest"), preview.indexOf('\n\n  const loadPersistence'))
  assert.match(incoming, /if \(externalBusy \|\| externalAgentBusy \|\| inspecting\) return/)
  assert.match(incoming, /requestExternalNavigation\(url, undefined, undefined, true\)\.then\(\(started\) => \{[\s\S]*if \(started\) handledExternalNavigationRef\.current/)
  assert.match(incoming, /\[externalAgentBusy, externalBusy, externalNavigationRequest, inspecting, requestExternalNavigation\]/)

  assert.match(preview, /aria-busy=\{paneInteractionBusy\}/)
  assert.match(preview, /role="tab" aria-selected=\{!browserMode\}[\s\S]*disabled=\{externalInteractionBusy\}[\s\S]*cancelExternalNavigation\(\)/)
  assert.match(preview, /commitExternalNavigation\(pendingExternalApproval, 'once'\)[\s\S]*commitExternalNavigation\(pendingExternalApproval, 'always'\)/)
})

test('external model actions acquire one synchronous exact-tab owner', () => {
  const handler = preview.slice(preview.indexOf("useEffect(() => EventsOn('external-browser:agent_request'"), preview.indexOf('\n\n  useEffect(() => {\n    if (!externalNavigationRequest'))
  assert.match(handler, /requestTabID !== activeExternalTab\.id \|\| requestToken !== activeExternalTab\.bridgeToken/)
  assert.match(handler, /const inFlight = externalAgentInFlightRef\.current/)
  assert.match(handler, /inFlight\.requestID === requestID && inFlight\.tabID === requestTabID && inFlight\.bridgeToken === requestToken/)
  assert.match(handler, /Another approved model action is already running/)
  assert.match(handler, /externalNavigationInFlightRef\.current \|\| closingExternalTabIDRef\.current === requestTabID/)
  assert.match(handler, /const owner: ExternalAgentActionOwner = \{[\s\S]*scope: scopeRef\.current\.generation,[\s\S]*requestID,[\s\S]*tabID: requestTabID,[\s\S]*bridgeToken: requestToken/)
  assert.ok(handler.indexOf('externalAgentInFlightRef.current = owner') < handler.indexOf('setExternalAgentBusy({ requestID, action })'))
  assert.match(handler, /current\?\.scope === owner\.scope[\s\S]*current\.requestID === owner\.requestID[\s\S]*current\.tabID === owner\.tabID[\s\S]*current\.bridgeToken === owner\.bridgeToken/)
  assert.match(handler, /if \(ownsScope\(owner\.scope\)\) setExternalAgentBusy/)
})

test('external tab close is single-flight and scope-owned', () => {
  const close = preview.slice(preview.indexOf('const closeExternalTab'), preview.indexOf("\n\n  useEffect(() => {\n    let active = true", preview.indexOf('const closeExternalTab')))
  assert.match(close, /externalNavigationInFlightRef\.current[\s\S]*externalAgentInFlightRef\.current[\s\S]*closingExternalTabIDRef\.current/)
  assert.ok(close.indexOf('closingExternalTabIDRef.current = tabID') < close.indexOf('await CloseExternalBrowserTab'))
  assert.match(close, /if \(!ownsScope\(scope\) \|\| closingExternalTabIDRef\.current !== tabID\) return false/)
  assert.match(close, /finally \{[\s\S]*ownsScope\(scope\) && closingExternalTabIDRef\.current === tabID[\s\S]*closingExternalTabIDRef\.current = null[\s\S]*setClosingExternalTabID\(null\)/)
  assert.match(preview, /closingExternalTabID === tab\.id \? <Loader2 size=\{10\} className="spin" \/> : <X size=\{10\} \/>/)
})

test('preview exposes one visible interaction lock across server and persistence controls', () => {
  assert.match(preview, /const externalInteractionBusy = busy \|\| persistenceBusy \|\| externalBusy \|\| externalAgentBusy !== null \|\| inspecting/)
  assert.match(preview, /const paneInteractionBusy = externalInteractionBusy \|\| closingExternalTabID !== null/)
  assert.match(preview, /aria-busy=\{paneInteractionBusy\}/)
  assert.match(preview, /disabled=\{configurations\.length === 0 \|\| starting \|\| running \|\| paneInteractionBusy\}/)
  assert.match(preview, /live-preview-notice action" role="status" aria-live="polite"/)
  assert.match(preview, /Updating this configuration’s isolated preview session data/)
  assert.match(preview, /disabled=\{paneInteractionBusy\}[\s\S]*Clear saved preview session data/)
  assert.match(preview, /Running one interaction in the exact visible Preview frame/)
})
