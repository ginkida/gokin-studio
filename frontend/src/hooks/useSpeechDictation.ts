import { useCallback, useEffect, useRef, useState } from 'react'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import {
  CancelSpeechDictation,
  GetSpeechDictationStatus,
  StartSpeechDictation,
  StopSpeechDictation,
} from '../../wailsjs/go/studio/Studio'

interface SpeechRecognitionAlternativeLike { transcript: string }
interface SpeechRecognitionResultLike {
  readonly isFinal: boolean
  readonly length: number
  readonly [index: number]: SpeechRecognitionAlternativeLike
}
interface SpeechRecognitionResultListLike {
  readonly length: number
  readonly [index: number]: SpeechRecognitionResultLike
}
interface SpeechRecognitionEventLike extends Event { readonly results: SpeechRecognitionResultListLike }
interface SpeechRecognitionErrorEventLike extends Event { readonly error: string; readonly message?: string }
interface SpeechRecognitionLike {
  continuous: boolean
  interimResults: boolean
  lang: string
  onstart: (() => void) | null
  onresult: ((event: SpeechRecognitionEventLike) => void) | null
  onerror: ((event: SpeechRecognitionErrorEventLike) => void) | null
  onend: (() => void) | null
  start(): void
  stop(): void
  abort(): void
}

type SpeechRecognitionConstructor = new () => SpeechRecognitionLike
type SpeechWindow = Window & {
  SpeechRecognition?: SpeechRecognitionConstructor
  webkitSpeechRecognition?: SpeechRecognitionConstructor
}

interface NativeSpeechEvent {
  sessionID: string
  type: 'authorizing' | 'started' | 'transcript' | 'stopping' | 'ended' | 'error'
  text?: string
  final?: boolean
  error?: string
  sequence?: number
}

export interface SpeechDictationState {
  supported: boolean
  listening: boolean
  interimTranscript: string
  error: string | null
  engine: 'native' | 'browser' | null
  phase: 'idle' | 'authorizing' | 'listening' | 'stopping'
  start: () => boolean
  stop: () => void
  cancel: () => void
  clearError: () => void
}

interface UseSpeechDictationOptions {
  language?: string
  onTranscript: (finalTranscript: string, interimTranscript: string) => void
}

function recognitionConstructor(): SpeechRecognitionConstructor | null {
  if (typeof window === 'undefined') return null
  const speechWindow = window as SpeechWindow
  return speechWindow.SpeechRecognition || speechWindow.webkitSpeechRecognition || null
}

function normalizeTranscript(parts: string[]): string {
  return parts.map((part) => part.replace(/\s+/g, ' ').trim()).filter(Boolean).join(' ')
}

function dictationErrorMessage(event: SpeechRecognitionErrorEventLike): string {
  switch (event.error) {
    case 'not-allowed':
    case 'service-not-allowed':
      return 'Microphone or speech-recognition access was denied. Allow it in system privacy settings, then try again.'
    case 'audio-capture': return 'No working microphone is available to the desktop runtime.'
    case 'network': return 'The platform speech-recognition service is currently unreachable.'
    case 'no-speech': return 'No speech was detected. Try again and speak after the listening indicator appears.'
    case 'language-not-supported': return 'The current system language is not supported by the platform speech-recognition service.'
    case 'bad-grammar': return 'The platform speech-recognition service rejected the recognition request.'
    case 'aborted': return 'Dictation was interrupted before recognition finished.'
    default: return event.message?.trim() || 'Voice dictation could not be started by this desktop runtime.'
  }
}

function newNativeSessionID(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return `speech-${crypto.randomUUID()}`
  return `speech-${Date.now()}-${Math.random().toString(36).slice(2)}`
}

// Native Speech.framework is preferred on macOS 14+. Browser/WebView speech
// remains a capability fallback elsewhere. Both engines expose transcript text
// only; microphone audio never enters React, draft storage, or model requests.
export function useSpeechDictation({ language, onTranscript }: UseSpeechDictationOptions): SpeechDictationState {
  const browserSupported = recognitionConstructor() !== null
  const [supported, setSupported] = useState(browserSupported)
  const [listening, setListening] = useState(false)
  const [interimTranscript, setInterimTranscript] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [engine, setEngine] = useState<'native' | 'browser' | null>(null)
  const [phase, setPhase] = useState<'idle' | 'authorizing' | 'listening' | 'stopping'>('idle')
  const recognitionRef = useRef<SpeechRecognitionLike | null>(null)
  const nativeSupportedRef = useRef<boolean | null>(null)
  const nativeSessionIDRef = useRef('')
  const nativeSequenceRef = useRef(0)
  const nativeStartPendingRef = useRef(false)
  const nativeStopRequestedRef = useRef<'stop' | 'cancel' | null>(null)
  const activeEngineRef = useRef<'pending' | 'native' | 'browser' | null>(null)
  const startAttemptRef = useRef(0)
  const onTranscriptRef = useRef(onTranscript)
  const languageRef = useRef(language)
  const suppressAbortErrorRef = useRef(false)

  useEffect(() => { onTranscriptRef.current = onTranscript }, [onTranscript])
  useEffect(() => { languageRef.current = language }, [language])

  useEffect(() => {
    let disposed = false
    Promise.resolve().then(() => GetSpeechDictationStatus()).then((status: any) => {
      if (disposed) return
      nativeSupportedRef.current = !!status?.supported
      setSupported(!!status?.supported || recognitionConstructor() !== null)
    }).catch(() => {
      if (disposed) return
      nativeSupportedRef.current = false
      setSupported(recognitionConstructor() !== null)
    })
    return () => { disposed = true }
  }, [])

  useEffect(() => {
    let off = () => {}
    try {
      off = EventsOn('speech-dictation:event', (event: NativeSpeechEvent) => {
        if (!event || event.sessionID !== nativeSessionIDRef.current) return
        const sequence = Number(event.sequence) || 0
        if (sequence && sequence <= nativeSequenceRef.current) return
        if (sequence) nativeSequenceRef.current = sequence
        if (event.type === 'authorizing' || event.type === 'started') {
          activeEngineRef.current = 'native'
          setEngine('native')
          setPhase(event.type === 'authorizing' ? 'authorizing' : 'listening')
          if (!nativeStopRequestedRef.current) setListening(true)
          return
        }
        if (event.type === 'transcript') {
          if (nativeStopRequestedRef.current === 'cancel') return
          const text = String(event.text || '')
          setInterimTranscript(event.final ? '' : text)
          onTranscriptRef.current(event.final ? text : '', event.final ? '' : text)
          return
        }
        if (event.type === 'stopping') {
          setPhase('stopping')
          setListening(false)
          return
        }
        if (event.type === 'error') {
          setError(String(event.error || 'Native speech recognition failed.'))
          setPhase('idle')
          setListening(false)
          return
        }
        if (event.type === 'ended') {
          nativeSessionIDRef.current = ''
          nativeStartPendingRef.current = false
          nativeStopRequestedRef.current = null
          activeEngineRef.current = null
          setListening(false)
          setInterimTranscript('')
          setEngine(null)
          setPhase('idle')
        }
      })
    } catch {
      // Browser preview and unsupported runtimes have no Wails event bridge.
    }
    return off
  }, [])

  const clearRecognition = useCallback((recognition: SpeechRecognitionLike) => {
    if (recognitionRef.current === recognition) recognitionRef.current = null
    recognition.onstart = null
    recognition.onresult = null
    recognition.onerror = null
    recognition.onend = null
  }, [])

  const startBrowser = useCallback((): boolean => {
    if (recognitionRef.current) return true
    const Constructor = recognitionConstructor()
    if (!Constructor) {
      activeEngineRef.current = null
      setListening(false)
      setSupported(false)
      setError('Voice dictation is not available in this desktop runtime. Use your operating system’s built-in dictation, or type the message.')
      setPhase('idle')
      return false
    }
    setError(null)
    setInterimTranscript('')
    suppressAbortErrorRef.current = false
    const recognition = new Constructor()
    recognition.continuous = true
    recognition.interimResults = true
    recognition.lang = languageRef.current || (typeof navigator !== 'undefined' ? navigator.language : '') || 'en-US'
    recognition.onstart = () => {
      if (recognitionRef.current !== recognition) return
      activeEngineRef.current = 'browser'
      setEngine('browser')
      setPhase('listening')
      setListening(true)
    }
    recognition.onresult = (event) => {
      if (recognitionRef.current !== recognition) return
      const finalParts: string[] = []
      const interimParts: string[] = []
      for (let resultIndex = 0; resultIndex < event.results.length; resultIndex++) {
        const result = event.results[resultIndex]
        if (!result || result.length === 0 || !result[0]) continue
        if (result.isFinal) finalParts.push(result[0].transcript)
        else interimParts.push(result[0].transcript)
      }
      const finalText = normalizeTranscript(finalParts)
      const interimText = normalizeTranscript(interimParts)
      setInterimTranscript(interimText)
      onTranscriptRef.current(finalText, interimText)
    }
    recognition.onerror = (event) => {
      if (recognitionRef.current !== recognition) return
      if (!(event.error === 'aborted' && suppressAbortErrorRef.current)) setError(dictationErrorMessage(event))
      clearRecognition(recognition)
      suppressAbortErrorRef.current = false
      activeEngineRef.current = null
      setListening(false)
      setInterimTranscript('')
      setEngine(null)
      setPhase('idle')
    }
    recognition.onend = () => {
      if (recognitionRef.current !== recognition) return
      clearRecognition(recognition)
      suppressAbortErrorRef.current = false
      activeEngineRef.current = null
      setListening(false)
      setInterimTranscript('')
      setEngine(null)
      setPhase('idle')
    }
    recognitionRef.current = recognition
    activeEngineRef.current = 'browser'
    setEngine('browser')
    setPhase('listening')
    try {
      setListening(true)
      recognition.start()
      return true
    } catch (startError) {
      clearRecognition(recognition)
      activeEngineRef.current = null
      setListening(false)
      setEngine(null)
      setPhase('idle')
      setError(startError instanceof Error && startError.message
        ? `Voice dictation could not start: ${startError.message}`
        : 'Voice dictation could not be started by this desktop runtime.')
      return false
    }
  }, [clearRecognition])

  const beginNative = useCallback((sessionID: string) => {
    nativeSessionIDRef.current = sessionID
    nativeSequenceRef.current = 0
    nativeStartPendingRef.current = true
    nativeStopRequestedRef.current = null
    activeEngineRef.current = 'native'
    setEngine('native')
    setPhase('authorizing')
    setListening(true)
    Promise.resolve().then(() => StartSpeechDictation(
      sessionID,
      languageRef.current || (typeof navigator !== 'undefined' ? navigator.language : '') || 'en-US',
    )).then(() => {
      if (nativeSessionIDRef.current !== sessionID) return
      nativeStartPendingRef.current = false
      const requested = nativeStopRequestedRef.current
      if (requested === 'cancel') {
        void CancelSpeechDictation(sessionID).catch(() => {}).finally(() => {
          if (nativeSessionIDRef.current === sessionID) nativeSessionIDRef.current = ''
          nativeStopRequestedRef.current = null
        })
      }
      if (requested === 'stop') void StopSpeechDictation(sessionID).catch((reason: unknown) => setError(String((reason as any)?.message || reason)))
    }).catch((reason: unknown) => {
      if (nativeSessionIDRef.current !== sessionID) return
      nativeSessionIDRef.current = ''
      nativeStartPendingRef.current = false
      nativeStopRequestedRef.current = null
      activeEngineRef.current = null
      setListening(false)
      setEngine(null)
      setPhase('idle')
      setError(`Native voice dictation could not start: ${String((reason as any)?.message || reason)}`)
    })
  }, [])

  const start = useCallback((): boolean => {
    if (activeEngineRef.current || recognitionRef.current || nativeSessionIDRef.current) return true
    const attempt = ++startAttemptRef.current
    activeEngineRef.current = 'pending'
    setError(null)
    setInterimTranscript('')
    setListening(true)
    const chooseEngine = async () => {
      let useNative = nativeSupportedRef.current
      if (useNative === null) {
        try {
          const status: any = await GetSpeechDictationStatus()
          useNative = !!status?.supported
          nativeSupportedRef.current = useNative
          setSupported(useNative || recognitionConstructor() !== null)
        } catch {
          useNative = false
          nativeSupportedRef.current = false
        }
      }
      if (startAttemptRef.current !== attempt || activeEngineRef.current !== 'pending') return
      if (useNative) beginNative(newNativeSessionID())
      else startBrowser()
    }
    void chooseEngine()
    return true
  }, [beginNative, startBrowser])

  const stop = useCallback(() => {
    if (activeEngineRef.current === 'pending') {
      startAttemptRef.current++
      activeEngineRef.current = null
      setListening(false)
      setEngine(null)
      setPhase('idle')
      return
    }
    const nativeID = nativeSessionIDRef.current
    if (nativeID) {
      nativeStopRequestedRef.current = 'stop'
      setListening(false)
      setPhase('stopping')
      if (!nativeStartPendingRef.current) {
        void StopSpeechDictation(nativeID).catch((reason: unknown) => setError(String((reason as any)?.message || reason)))
      }
      return
    }
    const recognition = recognitionRef.current
    if (!recognition) return
    try { recognition.stop() } catch {
      clearRecognition(recognition)
      activeEngineRef.current = null
      setListening(false)
      setInterimTranscript('')
      setEngine(null)
      setPhase('idle')
    }
  }, [clearRecognition])

  const cancel = useCallback(() => {
    startAttemptRef.current++
    const nativeID = nativeSessionIDRef.current
    if (nativeID) {
      nativeStopRequestedRef.current = 'cancel'
      if (!nativeStartPendingRef.current) {
        void CancelSpeechDictation(nativeID).catch(() => {}).finally(() => {
          if (nativeSessionIDRef.current === nativeID) nativeSessionIDRef.current = ''
          nativeStopRequestedRef.current = null
        })
      }
    }
    const recognition = recognitionRef.current
    if (recognition) {
      suppressAbortErrorRef.current = true
      try { recognition.abort() } catch { /* already ended */ }
      clearRecognition(recognition)
      suppressAbortErrorRef.current = false
    }
    activeEngineRef.current = null
    setListening(false)
    setInterimTranscript('')
    setEngine(null)
    setPhase('idle')
  }, [clearRecognition])

  useEffect(() => () => {
    startAttemptRef.current++
    const nativeID = nativeSessionIDRef.current
    if (nativeID) {
      nativeStopRequestedRef.current = 'cancel'
      void CancelSpeechDictation(nativeID).catch(() => {})
    }
    const recognition = recognitionRef.current
    if (!recognition) return
    recognition.onstart = null
    recognition.onresult = null
    recognition.onerror = null
    recognition.onend = null
    try { recognition.abort() } catch { /* runtime already closed it */ }
    recognitionRef.current = null
  }, [])

  return {
    supported,
    listening,
    interimTranscript,
    error,
    engine,
    phase,
    start,
    stop,
    cancel,
    clearError: useCallback(() => setError(null), []),
  }
}
