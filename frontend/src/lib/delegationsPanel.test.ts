import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

test('delegations panel participates in the shared modal and focus contract', () => {
  const panel = readFileSync(new URL('../components/dispatch/DelegationsPanel.tsx', import.meta.url), 'utf8')
  const styles = readFileSync(new URL('../App.css', import.meta.url), 'utf8')

  assert.match(panel, /className="app-dialog-backdrop"/)
  assert.match(panel, /role="dialog"/)
  assert.match(panel, /aria-modal="true"/)
  assert.match(panel, /aria-labelledby="delegations-title"/)
  assert.match(panel, /event\.key !== 'Escape'/)
  assert.match(panel, /aria-expanded=\{open\}/)
  assert.match(panel, /run\.kind !== 'ask' && run\.toSessionID/)
  assert.match(panel, /isTerminalDelegation\(run\.status\) && answers\[run\.id\] === undefined/)
  assert.match(styles, /\.delegations-load-error\s*\{/)
  assert.match(styles, /\.delegations-panel \.btn-small\s*\{/)
})

test('rare top-level surfaces are loaded on demand', () => {
  const app = readFileSync(new URL('../App.tsx', import.meta.url), 'utf8')

  assert.match(app, /const SettingsPage = lazy\(/)
  assert.match(app, /const DelegationsPanel = lazy\(/)
  assert.match(app, /const OnboardingWizard = lazy\(/)
  assert.match(app, /<Suspense fallback=/)
  assert.match(app, /className="app-dialog lazy-modal-loading"[\s\S]*aria-modal="true"/)
})
