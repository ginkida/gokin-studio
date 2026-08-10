import { useCallback, useEffect, useRef, useState } from 'react'
import { CheckCircle2, Circle, ListChecks, Loader2, RefreshCw, SkipForward, XCircle } from 'lucide-react'
import { GetSessionPlan } from '../../../wailsjs/go/studio/Studio'
import { EventsOn } from '../../../wailsjs/runtime/runtime'

interface PlanStep { title: string; status: string }
interface PlanInfo {
  active: boolean
  title?: string
  status?: string
  currentStep: number
  totalSteps: number
  steps?: PlanStep[]
}

function StepIcon({ status }: { status: string }) {
  if (status === 'completed') return <CheckCircle2 size={14} className="workspace-plan-done" />
  if (status === 'in_progress') return <Loader2 size={14} className="workspace-plan-active spin" />
  if (status === 'failed') return <XCircle size={14} className="workspace-plan-failed" />
  if (status === 'skipped') return <SkipForward size={14} className="workspace-plan-skipped" />
  return <Circle size={14} className="workspace-plan-pending" />
}

export function PlanPane({ projectId, sessionId }: { projectId: string; sessionId: string }) {
  const requestRef = useRef(0)
  const [plan, setPlan] = useState<PlanInfo | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(() => {
    const request = ++requestRef.current
    setLoading(true)
    GetSessionPlan(projectId, sessionId)
      .then((value: any) => {
        if (requestRef.current !== request) return
        setPlan((value || { active: false, currentStep: 0, totalSteps: 0 }) as PlanInfo)
        setError(null)
      })
      .catch((reason: any) => {
        if (requestRef.current === request) setError(String(reason?.message || reason))
      })
      .finally(() => { if (requestRef.current === request) setLoading(false) })
  }, [projectId, sessionId])

  useEffect(() => {
    load()
    const offComplete = EventsOn('chat:complete', (event: any) => {
      if (event?.projectID === projectId && (!event?.sessionID || event.sessionID === sessionId)) load()
    })
    const offStatus = EventsOn('project:status', (event: any) => {
      if (event?.projectID === projectId && event?.status === 'idle') load()
    })
    return () => { requestRef.current++; offComplete(); offStatus() }
  }, [load, projectId, sessionId])

  const steps = plan?.steps || []
  const progress = plan?.totalSteps ? Math.max(0, Math.min(100, Math.round((plan.currentStep / plan.totalSteps) * 100))) : 0
  return <div className="workspace-plan-pane">
    <header className="workspace-plan-header">
      <div><ListChecks size={14} /><span>Plan</span></div>
      <button type="button" onClick={load} disabled={loading} title="Refresh plan" aria-label="Refresh plan"><RefreshCw size={12} className={loading ? 'spin' : ''} /></button>
    </header>
    {error ? <div className="workspace-plan-state error"><span>{error}</span><button type="button" onClick={load}>Retry</button></div>
      : loading && !plan ? <div className="workspace-plan-state"><Loader2 size={16} className="spin" /> Loading plan…</div>
        : !plan?.active ? <div className="workspace-plan-state"><ListChecks size={22} /><strong>No active plan</strong><span>Multi-step agent work appears here as it progresses.</span></div>
          : <>
            <section className="workspace-plan-summary">
              <div><strong>{plan.title || 'Untitled plan'}</strong><span>{plan.currentStep}/{plan.totalSteps} steps · {plan.status || 'in progress'}</span></div>
              <div className="workspace-plan-progress" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={progress}><i style={{ width: `${progress}%` }} /></div>
            </section>
            <ol className="workspace-plan-steps">
              {steps.map((step, index) => <li key={`${index}:${step.title}`} className={`status-${step.status}`}>
                <StepIcon status={step.status} /><span><small>Step {index + 1}</small><strong>{step.title}</strong></span>
              </li>)}
            </ol>
          </>}
  </div>
}
