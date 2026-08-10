import { useEffect, useMemo, useRef, useState } from 'react'
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

export function MCPAppView({ payload }: { payload: MCPAppPayload }) {
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const initializedRef = useRef(false)
  const toolCallInFlightRef = useRef(false)
  const documentGenerationRef = useRef(0)
  const [theme, setTheme] = useState<'dark' | 'light'>(currentTheme)
  const [height, setHeight] = useState(320)
  const [fullscreen, setFullscreen] = useState(false)
  const [appCallStatus, setAppCallStatus] = useState<string | null>(null)
  const srcDoc = useMemo(() => buildMCPAppDocument(payload, theme), [payload, theme])
  const externalOrigins = (payload.csp?.connectDomains?.length || 0) +
    (payload.csp?.resourceDomains?.length || 0) +
    (payload.csp?.frameDomains?.length || 0)

  useEffect(() => {
    const observer = new MutationObserver(() => setTheme(currentTheme()))
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    return () => observer.disconnect()
  }, [])

  useEffect(() => {
    const respond = (id: string | number, result?: unknown, error?: { code: number; message: string }) => {
      const target = iframeRef.current?.contentWindow
      if (!target) return
      target.postMessage(error
        ? { jsonrpc: '2.0', id, error }
        : { jsonrpc: '2.0', id, result: result ?? {} }, '*')
    }
    const notifyToolData = () => {
      const target = iframeRef.current?.contentWindow
      if (!target || !initializedRef.current) return
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
    const onMessage = (event: MessageEvent) => {
      if (event.source !== iframeRef.current?.contentWindow) return
      const message = event.data
      if (!message || typeof message !== 'object' || message.jsonrpc !== '2.0') return
      const method = typeof message.method === 'string' ? message.method : ''
      const id = typeof message.id === 'string' || typeof message.id === 'number' ? message.id : null
      if ((method === 'ui/initialize' || method === 'initialize') && id !== null) {
        initializedRef.current = false
        respond(id, {
          protocolVersion: '2026-01-26',
          hostCapabilities: payload.instanceID ? {
            serverTools: { listChanged: false },
          } : {},
          hostInfo: { name: 'gokin-studio', version: '2.0.0' },
          hostContext: {
            toolInfo: {
              tool: { name: payload.toolName, inputSchema: { type: 'object' } },
            },
            theme,
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
        initializedRef.current = true
        notifyToolData()
        return
      }
      if (method === 'ui/notifications/size-changed') {
        const requested = Number(message.params?.height)
        if (Number.isFinite(requested)) setHeight(Math.max(180, Math.min(720, Math.round(requested))))
        return
      }
      if (method === 'notifications/message') return
      if (method === 'ping' && id !== null) {
        respond(id, {})
        return
      }
      if (method === 'ui/request-display-mode' && id !== null) {
        // App-originated fullscreen is deliberately denied; the surrounding
        // host button provides a user-gesture-only fullscreen path.
        respond(id, { mode: 'inline' })
        return
      }
      if (method === 'tools/call' && id !== null) {
        if (!initializedRef.current || !payload.instanceID) {
          respond(id, undefined, { code: -32002, message: 'MCP App tool calls are not available for this view' })
          return
        }
        if (toolCallInFlightRef.current) {
          respond(id, undefined, { code: -32003, message: 'Another app action is already waiting or running' })
          return
        }
        const name = typeof message.params?.name === 'string' ? message.params.name.trim() : ''
        const args = message.params?.arguments
        if (!name || name.length > 128 || !args || typeof args !== 'object' || Array.isArray(args)) {
          respond(id, undefined, { code: -32602, message: 'Invalid tools/call parameters' })
          return
        }
        let encodedSize = 0
        try {
          encodedSize = new TextEncoder().encode(JSON.stringify(args)).byteLength
        } catch {
          respond(id, undefined, { code: -32602, message: 'Tool arguments must be JSON-serializable' })
          return
        }
        if (encodedSize > 256 * 1024) {
          respond(id, undefined, { code: -32602, message: 'Tool arguments exceed the 256 KiB limit' })
          return
        }
        toolCallInFlightRef.current = true
        setAppCallStatus('Approval required')
        const generation = documentGenerationRef.current
        void CallMCPAppTool(payload.instanceID, name, args as Record<string, unknown>)
          .then((result) => {
            if (generation === documentGenerationRef.current) respond(id, result)
          })
          .catch((error: any) => {
            if (generation !== documentGenerationRef.current) return
            const message = String(error?.message || error || 'MCP App action failed').slice(0, 500)
            respond(id, undefined, { code: -32000, message })
          })
          .finally(() => {
            toolCallInFlightRef.current = false
            if (generation === documentGenerationRef.current) setAppCallStatus(null)
          })
        return
      }
      if (id !== null) {
        respond(id, undefined, { code: -32601, message: 'Capability is not enabled by this host' })
      }
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [payload, theme])

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
          <span className="mcp-app-call-status">
            <Loader2 size={10} className="spin" />
            {appCallStatus}
          </span>
        )}
        <button
          type="button"
          title={fullscreen ? 'Exit fullscreen' : 'Open fullscreen'}
          onClick={() => setFullscreen((value) => !value)}
        >
          {fullscreen ? <Minimize2 size={12} /> : <Maximize2 size={12} />}
        </button>
      </div>
      <iframe
        ref={iframeRef}
        className="mcp-app-frame"
        srcDoc={srcDoc}
        sandbox="allow-scripts"
        referrerPolicy="no-referrer"
        style={{ height: fullscreen ? 'calc(100vh - 86px)' : `${height}px` }}
        title={`MCP App ${payload.resourceURI}`}
        onLoad={() => {
          initializedRef.current = false
          documentGenerationRef.current += 1
          setAppCallStatus(null)
        }}
      />
    </div>
  )

  return view
}
