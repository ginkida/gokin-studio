import assert from 'node:assert/strict'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

// React identifies hooks by call order, so a hook that sits after an early
// return is skipped on the renders that take that branch. The next render with
// a different branch shifts every later hook by one slot and React throws
// "Rendered more hooks than during the previous render" — error #310 in the
// production build, which reaches the user as a blank screen rather than a
// broken widget.
//
// This project shipped that exact failure once: it compiled, type-checked, and
// passed the suite, because nothing here renders components. It was caught only
// by a person launching the app. A source scan is a blunt instrument, but it is
// the one check that runs without a DOM and covers all of ChatPanel.

const SRC = fileURLToPath(new URL('..', import.meta.url))

const HOOK = /\b(useState|useEffect|useMemo|useCallback|useRef|useLayoutEffect|useReducer|useContext|useSyncExternalStore|useTransition|useDeferredValue|useImperativeHandle|useId)\s*\(/
// A component or custom hook: capitalised name, or a name starting with "use".
const FUNC = /^(?:export\s+)?(?:default\s+)?function\s+([A-Z]\w*|use[A-Z]\w*)\s*\(/
// Only body-level statements (two-space indent) are considered, which is where
// a component's hooks and its guard clauses live. Anything deeper belongs to a
// nested callback, where a `return` is ordinary control flow, not a guard.
const BODY_RETURN = /^ {2}(if \(.*\)\s*return\b|return\b)/
const BODY_DECL = /^ {2}(const|let|var|use[A-Z])/

function tsxFiles(dir: string): string[] {
  const found: string[] = []
  for (const entry of readdirSync(dir)) {
    if (entry === 'node_modules') continue
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) {
      found.push(...tsxFiles(full))
    } else if (entry.endsWith('.tsx') && !entry.includes('.test.')) {
      found.push(full)
    }
  }
  return found
}

export function findHooksAfterEarlyReturn(source: string): Array<{ fn: string; returnLine: number; hookLine: number; code: string }> {
  const lines = source.split('\n')
  const findings: Array<{ fn: string; returnLine: number; hookLine: number; code: string }> = []
  let i = 0
  while (i < lines.length) {
    const start = FUNC.exec(lines[i])
    if (!start) {
      i++
      continue
    }
    let depth = 0
    let opened = false
    let earlyReturn: number | null = null
    let j = i
    for (; j < lines.length; j++) {
      const line = lines[j]
      for (const ch of line) {
        if (ch === '{') depth++
        else if (ch === '}') depth--
      }
      if (!opened && line.includes('{')) opened = true
      if (opened && depth >= 1 && earlyReturn === null && BODY_RETURN.test(line)) {
        earlyReturn = j + 1
      }
      if (earlyReturn !== null && HOOK.test(line) && BODY_DECL.test(line)) {
        findings.push({ fn: start[1], returnLine: earlyReturn, hookLine: j + 1, code: line.trim().slice(0, 100) })
      }
      if (opened && depth <= 0) break
    }
    i = j + 1
  }
  return findings
}

test('the detector recognises a hook after an early return', () => {
  const bad = [
    'export default function BadComponent({ items }: { items: string[] }) {',
    '  const [count, setCount] = useState(0)',
    '  if (!items.length) return null',
    '  const memo = useMemo(() => items.join(","), [items])',
    '  return <div>{memo}{count}</div>',
    '}',
  ].join('\n')
  const found = findHooksAfterEarlyReturn(bad)
  assert.equal(found.length, 1, 'a hook after a guard clause must be reported')
  assert.equal(found[0].hookLine, 4)

  const good = bad
    .replace('  if (!items.length) return null\n', '')
    .replace('  return <div>', '  if (!items.length) return null\n  return <div>')
  assert.deepEqual(findHooksAfterEarlyReturn(good), [], 'hooks before the guard must not be reported')
})

test('no component declares a hook after an early return', () => {
  const files = tsxFiles(SRC)
  assert.ok(files.length > 10, `expected to scan the component tree, found ${files.length} files`)
  const problems: string[] = []
  for (const file of files) {
    for (const f of findHooksAfterEarlyReturn(readFileSync(file, 'utf8'))) {
      problems.push(
        `${file.slice(SRC.length)}:${f.hookLine} ${f.fn} declares ${f.code} after the early return on line ${f.returnLine}`,
      )
    }
  }
  assert.deepEqual(problems, [], `hooks-after-early-return found:\n${problems.join('\n')}`)
})
