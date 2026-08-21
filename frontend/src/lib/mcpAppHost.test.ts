import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const host = readFileSync(new URL('../components/chat/MCPAppView.tsx', import.meta.url), 'utf8')

test('MCP App iframe identity covers document content and its native permission boundary', () => {
  assert.match(host, /const iframeIdentityRef = useRef\(\{[\s\S]*srcDoc,[\s\S]*instanceID: payload\.instanceID,[\s\S]*resourceURI: payload\.resourceURI,[\s\S]*toolName: payload\.toolName/)
  assert.match(host, /iframeIdentity\.srcDoc !== srcDoc \|\|[\s\S]*iframeIdentity\.instanceID !== payload\.instanceID \|\|[\s\S]*iframeIdentity\.resourceURI !== payload\.resourceURI \|\|[\s\S]*iframeIdentity\.toolName !== payload\.toolName/)
  assert.match(host, /<iframe[\s\S]*key=\{iframeKey\}[\s\S]*sandbox="allow-scripts"/)
  assert.doesNotMatch(host, /allow-same-origin/)

  const lifecycle = host.slice(host.indexOf('useLayoutEffect(() => {\n    documentGenerationRef'), host.indexOf('\n  useEffect(() => {\n    const respond'))
  assert.match(lifecycle, /documentGenerationRef\.current \+= 1[\s\S]*initializeGenerationRef\.current = null[\s\S]*initializedGenerationRef\.current = null[\s\S]*toolCallRef\.current = null/)
  assert.ok([...lifecycle.matchAll(/documentGenerationRef\.current \+= 1/g)].length >= 2)
})

test('MCP App bridge accepts messages only from the current frame and initialized load generation', () => {
  const bridge = host.slice(host.indexOf('const onMessage ='), host.indexOf("window.addEventListener('message'"))
  assert.match(bridge, /const target = iframeRef\.current\?\.contentWindow[\s\S]*if \(!target \|\| event\.source !== target\) return/)
  assert.match(bridge, /initializeGenerationRef\.current = generation[\s\S]*initializedGenerationRef\.current = null[\s\S]*respond\(target, id/)
  assert.match(bridge, /notifications\/initialized'[\s\S]*initializeGenerationRef\.current !== generation[\s\S]*initializeGenerationRef\.current = null[\s\S]*initializedGenerationRef\.current = generation/)
  assert.match(bridge, /notifications\/size-changed'[\s\S]*initializedGenerationRef\.current !== generation/)
  assert.match(bridge, /method === 'tools\/call'[\s\S]*initializedGenerationRef\.current !== generation \|\| !activePayload\.instanceID/)

  const load = host.slice(host.indexOf('onLoad={() => {'), host.indexOf('\n        }}', host.indexOf('onLoad={() => {')))
  assert.match(load, /documentGenerationRef\.current \+= 1[\s\S]*initializeGenerationRef\.current = null[\s\S]*initializedGenerationRef\.current = null[\s\S]*toolCallRef\.current = null/)
})

test('MCP App tool completion owns its document, contentWindow, and request token', () => {
  const call = host.slice(host.indexOf('const request = ++toolCallSequenceRef.current'), host.indexOf('\n        return', host.indexOf('const request = ++toolCallSequenceRef.current')))
  assert.match(call, /toolCallRef\.current = \{ generation, request \}/)
  assert.match(call, /const ownsCall = \(\) => \([\s\S]*documentGenerationRef\.current === generation[\s\S]*iframeRef\.current\?\.contentWindow === target[\s\S]*toolCallRef\.current\?\.generation === generation[\s\S]*toolCallRef\.current\?\.request === request/)
  assert.match(call, /\.then\(\(result\) => \{[\s\S]*if \(ownsCall\(\)\) respond\(target, id, result\)/)
  assert.match(call, /\.catch\([\s\S]*if \(!ownsCall\(\)\) return/)
  assert.match(call, /\.finally\(\(\) => \{[\s\S]*if \(!ownsCall\(\)\) return[\s\S]*toolCallRef\.current = null[\s\S]*setAppCallStatus\(null\)/)
  assert.doesNotMatch(host, /toolCallInFlightRef/)
})

test('MCP App bridge bounds RPC envelopes and exposes its busy/fullscreen state', () => {
  assert.match(host, /typeof value === 'string'\) return value\.length <= 128/)
  assert.match(host, /typeof value === 'number' && Number\.isFinite\(value\) && Math\.abs\(value\) <= Number\.MAX_SAFE_INTEGER/)
  assert.match(host, /message\.method\.length <= 128/)
  assert.match(host, /encodedSize > 256 \* 1024/)
  assert.match(host, /className="mcp-app-call-status" role="status" aria-live="polite"/)
  assert.match(host, /aria-label=\{fullscreen \? 'Exit MCP App fullscreen' : 'Open MCP App fullscreen'\}[\s\S]*aria-pressed=\{fullscreen\}/)
})
