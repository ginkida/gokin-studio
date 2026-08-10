import assert from 'node:assert/strict'
import test from 'node:test'
import {
  isInlineArtifactPath,
  isPreviewableFilePath,
  isStaticPreviewFilePath,
  normalizeMarkdownProjectPath,
  normalizeMarkdownDirectoryPath,
  normalizeMarkdownPreviewPath,
} from './previewFiles.ts'

test('static preview recognizes browser-viewable files without exposing source files', () => {
  for (const path of ['index.html', 'docs/report.pdf', 'shots/home.PNG', 'media/demo.webm', 'icon.svg']) {
    assert.equal(isStaticPreviewFilePath(path), true, path)
  }
  for (const path of ['main.go', '.env', 'package.json', 'notes.md', 'report.docx']) {
    assert.equal(isStaticPreviewFilePath(path), false, path)
  }
})

test('office files keep artifact routing while images avoid heavy inline cards', () => {
  assert.equal(isPreviewableFilePath('report.docx'), true)
  assert.equal(isInlineArtifactPath('report.docx'), true)
  assert.equal(isPreviewableFilePath('screen.webp'), true)
  assert.equal(isInlineArtifactPath('screen.webp'), false)
})

test('markdown preview paths stay bounded and leave absolute locations inert', () => {
  assert.equal(normalizeMarkdownPreviewPath('./dist/page.html'), 'dist/page.html')
  assert.equal(normalizeMarkdownPreviewPath('shots/home image.png'), 'shots/home image.png')
  assert.equal(normalizeMarkdownPreviewPath('https://example.com/page.html'), null)
  assert.equal(normalizeMarkdownPreviewPath('/tmp/page.html'), null)
  assert.equal(normalizeMarkdownPreviewPath('../outside.html'), null)
  assert.equal(normalizeMarkdownPreviewPath('line1.html\nline2.html'), null)
})

test('markdown project paths recognize source files without treating code expressions as navigation', () => {
  assert.equal(normalizeMarkdownProjectPath('src/main.go'), 'src/main.go')
  assert.equal(normalizeMarkdownProjectPath('./package.json'), 'package.json')
  assert.equal(normalizeMarkdownProjectPath('Dockerfile'), 'Dockerfile')
  assert.equal(normalizeMarkdownProjectPath('src/BUILD'), 'src/BUILD')
  assert.equal(normalizeMarkdownProjectPath('foo()'), null)
  assert.equal(normalizeMarkdownProjectPath('src/foo()'), null)
  assert.equal(normalizeMarkdownProjectPath('v1.2.3'), null)
  assert.equal(normalizeMarkdownProjectPath('../secret.txt'), null)
  assert.equal(normalizeMarkdownProjectPath('.git/config'), null)
  assert.equal(normalizeMarkdownProjectPath('.GIT/config'), null)
  assert.equal(normalizeMarkdownProjectPath('https://example.com/file.ts'), null)
})

test('markdown directory paths require an explicit safe trailing slash', () => {
  assert.equal(normalizeMarkdownDirectoryPath('src/'), 'src')
  assert.equal(normalizeMarkdownDirectoryPath('./src/components/'), 'src/components')
  assert.equal(normalizeMarkdownDirectoryPath('src'), null)
  assert.equal(normalizeMarkdownDirectoryPath('../src/'), null)
  assert.equal(normalizeMarkdownDirectoryPath('.git/hooks/'), null)
  assert.equal(normalizeMarkdownDirectoryPath('https://example.com/'), null)
})
