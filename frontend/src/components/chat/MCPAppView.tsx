import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { AppWindow, Loader2, Maximize2, Minimize2, ShieldCheck } from 'lucide-react'
import type { MCPAppPayload } from '../../stores/chatStore'
import { CallMCPAppTool } from '../../../wailsjs/go/studio/Studio'

const SAFE_ORIGIN = /^https:\/\/(?:\*\.)?[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?(?::[0-9]{1,5})?$/
const SAFE_CONNECT_ORIGIN = /^(?:https|wss):\/\/(?:\*\.)?[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?(?::[0-9]{1,5})?$/

function safeOrigins(values: string[] | undefined, connect = false) {
  const matcher = connect ? SAFE_CONNECT_ORIGIN : SAFE_ORIGIN
  return (values || []).filter((value) => value.length <= 512 && matcher.test(value)).slice(0, 32)
}

export function buildMCPAppDocument(payload: MCPAppPayload, theme: 'dark' | 'light') {
  const connect = safeOrigins(payload.csp?.connectDomains, true)
  const resources = safeOrigins(payload.csp?.resourceDomains)
  const frames = safeOrigins(payload.csp?.frameDomains)
  const resourceSuffix = resources.length > 0 ? ` ${resources.join(' ')}` : ''
  const policy = [
    "default-src 'none'",
    `script-src 'unsafe-inline'${resourceSuffix}`,
    `style-src 'unsafe-inline'${resourceSuffix}`,
    `img-src data: blob:${resourceSuffix}`,
    `font-src data:${resourceSuffix}`,
    `media-src data: blob:${resourceSuffix}`,
    `connect-src ${connect.length > 0 ? connect.join(' ') : "'none'"}`,
    `frame-src ${frames.length > 0 ? frames.join(' ') : "'none'"}`,
    "worker-src 'none'",
    "object-src 'none'",
    "base-uri 'none'",
    "form-action 'none'",
    "navigate-to 'none'",
  ].join('; ')
  const colors = theme === 'dark'
    ? { bg: '#17181c', surface: '#202126', text: '#f2f3f5', muted: '#a0a4ad', border: '#35373e', accent: '#8b7cf6' }
    : { bg: '#ffffff', surface: '#f5f6f8', text: '#191a1e', muted: '#646873', border: '#dedfe4', accent: '#6657d9' }
  const themeCSS = `:root{color-scheme:${theme};--color-background-primary:${colors.bg};--color-background-secondary:${colors.surface};--color-text-primary:${colors.text};--color-text-secondary:${colors.muted};--color-border-primary:${colors.border};--color-accent-primary:${colors.accent};--font-sans:Inter,system-ui,sans-serif}html,body{min-height:100%;margin:0;background:${colors.bg};color:${colors.text};font-family:var(--font-sans)}`
  return `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="${policy}"><meta name="referrer" content="no-referrer"><style>${themeCSS}</style></head><body>${payload.html}</body></html>`
}

function currentTheme(): 'dark' | 'light' {
  const selected = document.documentElement.getAttribute('data-theme')
  if (selected === 'light') return 'light'
  if (selected === 'system' && window.matchMedia?.('(prefers-color-scheme: light)').matches) return 'light'
  return 'dark'
}

function isRPCID(value: unknown): value is string | number {
  if (typeof value === 'string') return value.length <= 128
  return typeof value === 'number' && Number.isFinite(value) && Math.abs(value) <= Number.MAX_SAFE_INTEGER
}

function postToolData(target: Window, payload: MCPAppPayload) {
  target.postMessage({
    jsonrpc: '2.0',
    method: 'ui/notifications/tool-input',
    params: { arguments: payload.toolArgs || {} },
  }, '*')
  target.postMessage({
    jsonrpc: '2.0',
    method: 'ui/notifications/tool-result',
    params: payload.toolResult || {},
  }, '*')
}

export function MCPAppView({ payload }: { payload: MCPAppPayload }) {
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const documentGenerationRef = useRef(0)
  const initializeGenerationRef = useRef<number | null>(null)
  const initializedGenerationRef = useRef<number | null>(null)
  const toolCallSequenceRef = useRef(0)
  const toolCallRef = useRef<{ generation: number; request: number } | null>(null)
  const [theme, setTheme] = useState<'dark' | 'light'>(currentTheme)
  const [height, setHeight] = useState(320)
  const [fullscreen, setFullscreen] = useState(false)
  const [appCallStatus, setAppCallStatus] = useState<string | null>(null)
  const srcDoc = useMemo(() => buildMCPAppDocument(payload, theme), [payload, theme])
  const payloadRef = useRef(payload)
  const themeRef = useRef(theme)
  const iframeIdentityRef = useRef({
    key: 0,
    srcDoc,
    instanceID: payload.instanceID,
    resourceURI: payload.resourceURI,
    toolName: payload.toolName,
  })
  const iframeIdentity = iframeIdentityRef.current
  if (
    iframeIdentity.srcDoc !== srcDoc ||
    iframeIdentity.instanceID !== payload.instanceID ||
    iframeIdentity.resourceURI !== payload.resourceURI ||
    iframeIdentity.toolName !== payload.toolName
  ) {
    iframeIdentityRef.current = {
      key: iframeIdentity.key + 1,
      srcDoc,
      instanceID: payload.instanceID,
      resourceURI: payload.resourceURI,
      toolName: payload.toolName,
    }
  }
  const iframeKey = iframeIdentityRef.current.key
  const externalOrigins = safeOrigins(payload.csp?.connectDomains, true).length +
    safeOrigins(payload.csp?.resourceDomains).length +
    safeOrigins(payload.csp?.frameDomains).length

  useEffect(() => {
    const observer = new MutationObserver(() => setTheme(currentTheme()))
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    return () => observer.disconnect()
  }, [])

  useLayoutEffect(() => {
    // Message handlers must see only committed props. Updating these refs
    // during render would expose an abandoned concurrent render to the live
    // iframe before React commits its replacement.
    payloadRef.current = payload
    themeRef.current = theme
  }, [payload, theme])

  useLayoutEffect(() => {
    documentGenerationRef.current += 1
    initializeGenerationRef.current = null
    initializedGenerationRef.current = null
    toolCallRef.current = null
    setAppCallStatus(null)
    return () => {
      documentGenerationRef.current += 1
      initializeGenerationRef.current = null
      initializedGenerationRef.current = null
      toolCallRef.current = null
    }
  }, [iframeKey])

  useEffect(() => {
    const respond = (target: Window, id: string | number, result?: unknown, error?: { code: number; message: string }) => {
      target.postMessage(error
        ? { jsonrpc: '2.0', id, error }
        : { jsonrpc: '2.0', id, result: result ?? {} }, '*')
    }
    const onMessage = (event: MessageEvent) => {
      const target = iframeRef.current?.contentWindow
      if (!target || event.source !== target) return
      const generation = documentGenerationRef.current
      const activePayload = payloadRef.current
      const message = event.data
      if (!message || typeof message !== 'object' || message.jsonrpc !== '2.0') return
      const method = typeof message.method === 'string' && message.method.length <= 128 ? message.method : ''
      const id = isRPCID(message.id) ? message.id : null
      if ((method === 'ui/initialize' || method === 'initialize') && id !== null) {
        initializeGenerationRef.current = generation
        initializedGenerationRef.current = null
        respond(target, id, {
          protocolVersion: '2026-01-26',
          hostCapabilities: activePayload.instanceID ? {
            serverTools: { listChanged: false },
          } : {},
          hostInfo: { name: 'gokin-studio', version: '2.0.0' },
          hostContext: {
            toolInfo: {
              tool: { name: activePayload.toolName, inputSchema: { type: 'object' } },
            },
            theme: themeRef.current,
            displayMode: 'inline',
            availableDisplayModes: ['inline'],
            containerDimensions: { maxWidth: 960, maxHeight: 720 },
            locale: navigator.language,
            timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
            userAgent: 'gokin-studio',
            platform: 'desktop',
            deviceCapabilities: {
              touch: navigator.maxTouchPoints > 0,
              hover: window.matchMedia?.('(hover: hover)').matches ?? true,
            },
          },
        })
        return
      }
      if (method === 'ui/notifications/initialized' || method === 'notifications/initialized') {
        if (initializeGenerationRef.current !== generation) return
        initializeGenerationRef.current = null
        initializedGenerationRef.current = generation
        postToolData(target, activePayload)
        return
      }
      if (method === 'ui/notifications/size-changed') {
        if (initializedGenerationRef.current !== generation) return
        const requested = Number(message.params?.height)
        if (Number.isFinite(requested)) setHeight(Math.max(180, Math.min(720, Math.round(requested))))
        return
      }
      if (method === 'notifications/message') return
      if (method === 'ping' && id !== null) {
        respond(target, id, {})
        return
      }
      if (method === 'ui/request-display-mode' && id !== null) {
        // App-originated fullscreen is deliberately denied; the surrounding
        // host button provides a user-gesture-only fullscreen path.
        respond(target, id, { mode: 'inline' })
        return
      }
      if (method === 'tools/call' && id !== null) {
        if (initializedGenerationRef.current !== generation || !activePayload.instanceID) {
          respond(target, id, undefined, { code: -32002, message: 'MCP App tool calls are not available for this view' })
          return
        }
        if (toolCallRef.current?.generation === generation) {
          respond(target, id, undefined, { code: -32003, message: 'Another app action is already waiting or running' })
          return
        }
        const name = typeof message.params?.name === 'string' ? message.params.name.trim() : ''
        const args = message.params?.arguments
        if (!name || name.length > 128 || !args || typeof args !== 'object' || Array.isArray(args)) {
          respond(target, id, undefined, { code: -32602, message: 'Invalid tools/call parameters' })
          return
        }
        let encodedSize = 0
        try {
          encodedSize = new TextEncoder().encode(JSON.stringify(args)).byteLength
        } catch {
          respond(target, id, undefined, { code: -32602, message: 'Tool arguments must be JSON-serializable' })
          return
        }
        if (encodedSize > 256 * 1024) {
          respond(target, id, undefined, { code: -32602, message: 'Tool arguments exceed the 256 KiB limit' })
          return
        }
        const request = ++toolCallSequenceRef.current
        toolCallRef.current = { generation, request }
        setAppCallStatus('Approval required')
        const ownsCall = () => (
          documentGenerationRef.current === generation &&
          iframeRef.current?.contentWindow === target &&
          toolCallRef.current?.generation === generation &&
          toolCallRef.current?.request === request
        )
        void CallMCPAppTool(activePayload.instanceID, name, args as Record<string, unknown>)
          .then((result) => {
            if (ownsCall()) respond(target, id, result)
          })
          .catch((error: any) => {
            if (!ownsCall()) return
            const message = String(error?.message || error || 'MCP App action failed').slice(0, 500)
            respond(target, id, undefined, { code: -32000, message })
          })
          .finally(() => {
            if (!ownsCall()) return
            toolCallRef.current = null
            setAppCallStatus(null)
          })
        return
      }
      if (id !== null) {
        respond(target, id, undefined, { code: -32601, message: 'Capability is not enabled by this host' })
      }
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [])

  useEffect(() => {
    const generation = documentGenerationRef.current
    const target = iframeRef.current?.contentWindow
    if (!target || initializedGenerationRef.current !== generation) return
    postToolData(target, payload)
  }, [payload.toolArgs, payload.toolResult])

  const view = (
    <div className={`mcp-app-card ${payload.prefersBorder ? 'prefers-border' : ''} ${fullscreen ? 'fullscreen' : ''}`} onClick={(e) => e.stopPropagation()}>
      <div className="mcp-app-toolbar">
        <AppWindow size={12} />
        <strong>MCP App</strong>
        <code>{payload.resourceURI}</code>
        <span className={`mcp-app-network ${externalOrigins > 0 ? 'external' : ''}`}>
          <ShieldCheck size={10} />
          {externalOrigins > 0 ? `${externalOrigins} declared origins` : 'offline sandbox'}
        </span>
        {appCallStatus && (
          <span className="mcp-app-call-status" role="status" aria-live="polite">
            <Loader2 size={10} className="spin" />
            {appCallStatus}
          </span>
        )}
        <button
          type="button"
          title={fullscreen ? 'Exit fullscreen' : 'Open fullscreen'}
          aria-label={fullscreen ? 'Exit MCP App fullscreen' : 'Open MCP App fullscreen'}
          aria-pressed={fullscreen}
          onClick={() => setFullscreen((value) => !value)}
        >
          {fullscreen ? <Minimize2 size={12} /> : <Maximize2 size={12} />}
        </button>
      </div>
      <iframe
        key={iframeKey}
        ref={iframeRef}
        className="mcp-app-frame"
        srcDoc={srcDoc}
        sandbox="allow-scripts"
        referrerPolicy="no-referrer"
        style={{ height: fullscreen ? 'calc(100vh - 86px)' : `${height}px` }}
        title={`MCP App ${payload.resourceURI}`}
        onLoad={() => {
          documentGenerationRef.current += 1
          initializeGenerationRef.current = null
          initializedGenerationRef.current = null
          toolCallRef.current = null
          setAppCallStatus(null)
        }}
      />
    </div>
  )

  return view
}
