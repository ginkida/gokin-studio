import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

// The name inputs bound their value with maxLength, which the browser counts in
// UTF-16 code units — characters, for every name a person actually types. The
// backend has to bound the same field in the same unit. It used to cap bytes at
// the same number, so a 60-character Cyrillic project name was accepted by the
// form and stored as 30 characters, cut mid-word, silently.
//
// This pins the two sides together: the browser limit and the Go limit must
// stay the same number, and the Go side must keep measuring characters. Neither
// half is verifiable from the other's tests, which is exactly why the mismatch
// survived so long.

const goSource = (file: string) => readFileSync(new URL(`../../../internal/studio/${file}`, import.meta.url), 'utf8')
const uiSource = (file: string) => readFileSync(new URL(`../${file}`, import.meta.url), 'utf8')

function displayNameMaxRunes(): number {
  const drafts = goSource('drafts.go')
  const match = drafts.match(/const DisplayNameMaxRunes = (\d+)/)
  assert.ok(match, 'DisplayNameMaxRunes is no longer declared in drafts.go')
  return Number(match![1])
}

test('the Go name cap is expressed in characters, not bytes', () => {
  const limit = displayNameMaxRunes()
  assert.ok(limit > 0, 'DisplayNameMaxRunes must be positive')

  const drafts = goSource('drafts.go')
  assert.match(
    drafts,
    /func truncateRunes\(s string, maxRunes int\) string/,
    'truncateRunes must remain the character-counting helper',
  )

  // Any display-name site that regressed to a byte cap of the same number would
  // reintroduce the halving, so no file may pair truncateUTF8 with the limit.
  for (const file of [
    'app.go',
    'config.go',
    'archived_projects.go',
    'project_export.go',
    'session_export.go',
  ]) {
    const src = goSource(file)
    assert.ok(
      !src.includes(`truncateUTF8(name, ${limit})`) &&
        !src.includes(`truncateUTF8(newName, ${limit})`),
      `${file} caps a display name in bytes again — it must use truncateRunes`,
    )
  }
})

test('every name input bounds the same number of characters the backend keeps', () => {
  const limit = displayNameMaxRunes()
  // Each entry is a surface a person types a project or chat name into.
  const surfaces: Array<[string, string]> = [
    ['components/layout/Sidebar.tsx', 'project add and rename'],
    ['App.tsx', 'chat session rename'],
    ['components/onboarding/OnboardingWizard.tsx', 'first project in onboarding'],
  ]
  for (const [file, what] of surfaces) {
    const src = uiSource(file)
    assert.ok(
      src.includes(`maxLength={${limit}}`),
      `${what} (${file}) no longer bounds names at ${limit} characters; the backend keeps ${limit}`,
    )
  }
})
