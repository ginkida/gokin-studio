import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const source = readFileSync(new URL('../components/chat/ScheduledTasksModal.tsx', import.meta.url), 'utf8')

test('scheduled task snapshots accept only the latest request in the current workspace scope', () => {
  assert.match(source, /const taskLoadRequestRef = useRef\(0\)/)
  assert.match(source, /const scopeKey = `\$\{projectID\}\\u0000\$\{sessionID\}`/)
  assert.match(source, /scopeRef\.current\.generation \+= 1[\s\S]*const generation = scopeRef\.current\.generation/)
  assert.match(source, /scopeRef\.current\.generation === generation[\s\S]*scopeRef\.current\.mounted = false[\s\S]*taskLoadRequestRef\.current \+= 1[\s\S]*historyRequestRef\.current \+= 1/)

  const load = source.slice(source.indexOf('const load = useCallback'), source.indexOf('const editing = useMemo'))
  assert.match(load, /const request = \+\+taskLoadRequestRef\.current/)
  assert.match(load, /!ownsScope\(scope\) \|\| taskLoadRequestRef\.current !== request/)
  assert.match(load, /finally \{[\s\S]*taskLoadRequestRef\.current === request[\s\S]*setLoading\(false\)/)
})

test('scheduled run history selects immediately and polls sequentially without stale writes', () => {
  const history = source.slice(source.indexOf('const openRunHistory'), source.indexOf('const openRun = async'))
  assert.match(history, /setHistoryTaskID\(taskID\)[\s\S]*setRunsLoading\(true\)/)
  assert.match(history, /const request = \+\+historyRequestRef\.current/)
  assert.match(history, /await ListScheduledTaskRuns\(projectID, taskID\)[\s\S]*historyRequestRef\.current !== request/)
  assert.match(history, /finally \{[\s\S]*setTimeout\(\(\) => \{ void poll\(false\) \}, 2000\)/)
  assert.doesNotMatch(history, /setInterval/)
  assert.match(history, /return \(\) => \{[\s\S]*stopped = true[\s\S]*clearTimeout/)
})

test('late scheduled task operations cannot navigate or mutate a replaced workspace', () => {
  const runNow = source.slice(source.indexOf('const runNow = async'), source.indexOf('const openRunHistory'))
  const openRun = source.slice(source.indexOf('const openRun = async'), source.indexOf('const remove = async'))
  const deletionStart = source.indexOf('const remove = async')
  const deletion = source.slice(deletionStart, source.indexOf('\n  return (', deletionStart))

  assert.match(runNow, /const scope = scopeRef\.current\.generation[\s\S]*await RunScheduledTaskNow[\s\S]*if \(!ownsScope\(scope\)\) return/)
  assert.match(runNow, /openRun\(run\.sessionID, scope\)/)
  assert.match(openRun, /await confirmDiscardDraft\(\)[\s\S]*if \(!ownsScope\(expectedScope\)\) return[\s\S]*gokin:switch-tab/)
  assert.match(deletion, /await GetScheduledTaskDeletionPreview[\s\S]*if \(!ownsScope\(scope\)\) return/)
  assert.match(deletion, /await DeleteScheduledTaskWithData[\s\S]*if \(!ownsScope\(scope\)\) return/)
})

test('scheduler load, history, and action failures have distinct live regions', () => {
  assert.match(source, /tasksError[\s\S]*className="scheduled-list-error" role="alert"/)
  assert.match(source, /className="scheduled-runs" aria-busy=\{runsLoading\}/)
  assert.match(source, /runsError[\s\S]*className="scheduled-runs-error" role="alert"/)
  assert.match(source, /className="scheduled-error" role="alert"/)
})
