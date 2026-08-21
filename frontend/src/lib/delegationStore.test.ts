import assert from 'node:assert/strict'
import test from 'node:test'
import {
  mergeHydratedDelegationRuns,
  type DelegationRun,
	useDelegationStore,
} from '../stores/delegationStore.ts'

function run(id: string, status: string, extra: Partial<DelegationRun> = {}): DelegationRun {
  return {
    id,
    kind: 'run',
    fromProjectID: 'source',
    fromSessionID: 'chat',
    toProjectID: 'target',
    status,
    startedAt: 100,
    ...extra,
  }
}

test('stale hydration cannot resurrect a terminal delegation', () => {
  const completed = run('one', 'completed', { completedAt: 300, answer: 'done' })
  const staleSnapshot = run('one', 'running', { startedAt: 90 })

  const merged = mergeHydratedDelegationRuns({ one: completed }, [staleSnapshot])

  assert.equal(merged.one.status, 'completed')
  assert.equal(merged.one.answer, 'done')
  assert.equal(merged.one.completedAt, 300)
})

test('terminal hydration reconciles a missed completion event', () => {
  const live = run('one', 'running', { goal: 'fresh event goal', startedAt: 120 })
  const completed = run('one', 'completed', {
    answer: 'durable full answer',
    completedAt: 300,
    progressTail: ['last tool result'],
    estimatedCostUSD: 0.25,
  })

  const merged = mergeHydratedDelegationRuns({ one: live }, [completed])

  assert.equal(merged.one.status, 'completed')
  assert.equal(merged.one.answer, 'durable full answer')
  assert.deepEqual(merged.one.progressTail, ['last tool result'])
  assert.equal(merged.one.estimatedCostUSD, 0.25)
})

test('live events keep precedence while hydration enriches missing fields', () => {
  const live = run('one', 'running', {
    goal: 'latest goal',
    startedAt: 200,
    // applyEvent materializes optional keys even when an event omitted them;
    // those undefined values must not erase richer durable fields.
    progressTail: undefined,
  })
  const durable = run('one', 'running', {
    task: 'persisted task',
    startedAt: 100,
    progressTail: ['persisted progress'],
  })

  const merged = mergeHydratedDelegationRuns({ one: live }, [durable])

  assert.equal(merged.one.goal, 'latest goal')
  assert.equal(merged.one.task, 'persisted task')
  assert.equal(merged.one.startedAt, 100)
  assert.deepEqual(merged.one.progressTail, ['persisted progress'])
})

test('a late started event cannot resurrect a terminal delegation', () => {
	useDelegationStore.setState({ runs: {}, tails: {}, names: {} })
	const applyEvent = useDelegationStore.getState().applyEvent
	applyEvent({
		runID: 'one', kind: 'run', fromProjectID: 'source', fromSessionID: 'chat',
		toProjectID: 'target', status: 'stopped', errorType: 'cancelled',
	})
	applyEvent({
		runID: 'one', kind: 'run', fromProjectID: 'source', fromSessionID: 'chat',
		toProjectID: 'target', status: 'running', goal: 'late start',
	})

	const stored = useDelegationStore.getState().runs.one
	assert.equal(stored.status, 'stopped')
	assert.equal(stored.errorType, 'cancelled')
	assert.equal(stored.goal, 'late start')
})
