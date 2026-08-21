import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import {
  BLOB_DOWNLOAD_REVOKE_DELAY_MS,
  downloadBlob,
  type BlobDownloadRuntime,
} from './download.ts'

function downloadFixture(overrides: Partial<BlobDownloadRuntime> = {}) {
  const events: string[] = []
  let deferred: (() => void) | undefined
  const anchor = {
    href: '',
    download: '',
    click: () => { events.push('click') },
    remove: () => { events.push('remove') },
  }
  const runtime: BlobDownloadRuntime = {
    createObjectURL: () => {
      events.push('create-url')
      return 'blob:test-download'
    },
    revokeObjectURL: (url) => { events.push(`revoke:${url}`) },
    createAnchor: () => {
      events.push('create-anchor')
      return anchor
    },
    appendAnchor: () => { events.push('append') },
    defer: (callback, delayMs) => {
      events.push(`defer:${delayMs}`)
      deferred = callback
    },
    ...overrides,
  }
  return { anchor, events, runtime, runDeferred: () => deferred?.() }
}

test('downloadBlob dispatches from an attached anchor and revokes only after a delay', () => {
  const fixture = downloadFixture()

  downloadBlob(new Blob(['payload']), 'report.csv', fixture.runtime)

  assert.equal(fixture.anchor.href, 'blob:test-download')
  assert.equal(fixture.anchor.download, 'report.csv')
  assert.deepEqual(fixture.events, [
    'create-url',
    'create-anchor',
    'append',
    'click',
    'remove',
    `defer:${BLOB_DOWNLOAD_REVOKE_DELAY_MS}`,
  ])

  fixture.runDeferred()
  assert.equal(fixture.events.at(-1), 'revoke:blob:test-download')
})

test('downloadBlob still removes and schedules cleanup when dispatch fails', () => {
  const fixture = downloadFixture({
    appendAnchor: () => { throw new Error('DOM unavailable') },
  })

  assert.throws(
    () => downloadBlob(new Blob(['payload']), 'report.csv', fixture.runtime),
    /DOM unavailable/,
  )
  assert.deepEqual(fixture.events, [
    'create-url',
    'create-anchor',
    'remove',
    `defer:${BLOB_DOWNLOAD_REVOKE_DELAY_MS}`,
  ])

  fixture.runDeferred()
  assert.equal(fixture.events.at(-1), 'revoke:blob:test-download')
})

test('downloadBlob falls back to immediate cleanup when timers are unavailable', () => {
  const fixture = downloadFixture({
    defer: () => { throw new Error('timer unavailable') },
  })

  downloadBlob(new Blob(['payload']), 'report.csv', fixture.runtime)

  assert.equal(fixture.events.at(-1), 'revoke:blob:test-download')
  assert.equal(fixture.events.filter((event) => event.startsWith('revoke:')).length, 1)
})

test('downloadBlob schedules URL cleanup even when removing the anchor fails', () => {
  const fixture = downloadFixture()
  fixture.anchor.remove = () => {
    fixture.events.push('remove')
    throw new Error('remove failed')
  }

  assert.throws(
    () => downloadBlob(new Blob(['payload']), 'report.csv', fixture.runtime),
    /remove failed/,
  )
  assert.equal(fixture.events.at(-1), `defer:${BLOB_DOWNLOAD_REVOKE_DELAY_MS}`)

  fixture.runDeferred()
  assert.equal(fixture.events.at(-1), 'revoke:blob:test-download')
})

test('download surfaces delegate temporary URL ownership to downloadBlob', () => {
  const consumers = [
    '../components/chat/ChatPanel.tsx',
    '../components/files/ArtifactPreview.tsx',
    '../components/files/InlineArtifactCard.tsx',
    '../components/layout/Sidebar.tsx',
    '../components/settings/SettingsPage.tsx',
  ]

  for (const path of consumers) {
    const source = readFileSync(new URL(path, import.meta.url), 'utf8')
    assert.match(source, /downloadBlob\(/, `${path} should use the shared downloader`)
    assert.doesNotMatch(source, /URL\.(?:create|revoke)ObjectURL/, `${path} should not own temporary URLs`)
  }
})
