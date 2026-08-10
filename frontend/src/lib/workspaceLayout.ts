export const WORKSPACE_PANE_IDS = ['chat', 'diff', 'preview', 'terminal', 'files', 'artifacts', 'plan', 'tasks', 'context'] as const

export type WorkspacePaneID = typeof WORKSPACE_PANE_IDS[number]
export type WorkspaceSplitAxis = 'horizontal' | 'vertical'
export type WorkspaceDropEdge = 'left' | 'right' | 'top' | 'bottom'

export interface WorkspacePaneNode {
  kind: 'pane'
  pane: WorkspacePaneID
}

export interface WorkspaceSplitNode {
  kind: 'split'
  axis: WorkspaceSplitAxis
  ratio: number
  first: WorkspaceLayoutNode
  second: WorkspaceLayoutNode
}

export type WorkspaceLayoutNode = WorkspacePaneNode | WorkspaceSplitNode

export interface WorkspaceLayout {
  version: 2
  root: WorkspaceLayoutNode
  /** Leaf traversal followed by closed panes. Kept for stable menus and v1 callers. */
  order: WorkspacePaneID[]
  /** Open panes in visual leaf traversal order. */
  open: WorkspacePaneID[]
}

interface LegacyWorkspaceLayout {
  order?: unknown
  open?: unknown
  widths?: Partial<Record<WorkspacePaneID, number>>
}

const PANE_SET = new Set<string>(WORKSPACE_PANE_IDS)
const DEFAULT_ORDER: WorkspacePaneID[] = ['chat', 'diff', 'preview', 'terminal', 'files', 'artifacts', 'plan', 'tasks', 'context']
const DEFAULT_LEGACY_WIDTHS: Record<WorkspacePaneID, number> = {
  chat: 720,
  diff: 760,
  preview: 560,
  terminal: 520,
  files: 420,
  artifacts: 520,
  plan: 360,
  tasks: 760,
  context: 304,
}

export const WORKSPACE_SPLIT_MIN_RATIO = 0.16
export const WORKSPACE_SPLIT_MAX_RATIO = 0.84

function paneList(value: unknown): WorkspacePaneID[] {
  if (!Array.isArray(value)) return []
  const seen = new Set<string>()
  const result: WorkspacePaneID[] = []
  for (const item of value) {
    if (typeof item !== 'string' || !PANE_SET.has(item) || seen.has(item)) continue
    seen.add(item)
    result.push(item as WorkspacePaneID)
  }
  return result
}

function paneNode(pane: WorkspacePaneID): WorkspacePaneNode {
  return { kind: 'pane', pane }
}

export function clampWorkspaceSplitRatio(value: number): number {
  if (!Number.isFinite(value)) return 0.5
  return Math.max(WORKSPACE_SPLIT_MIN_RATIO, Math.min(WORKSPACE_SPLIT_MAX_RATIO, Math.round(value * 10000) / 10000))
}

function splitNode(axis: WorkspaceSplitAxis, ratio: number, first: WorkspaceLayoutNode, second: WorkspaceLayoutNode): WorkspaceSplitNode {
  return { kind: 'split', axis, ratio: clampWorkspaceSplitRatio(ratio), first, second }
}

function parseNode(value: unknown, seen: Set<WorkspacePaneID>, depth = 0): WorkspaceLayoutNode | null {
  if (!value || typeof value !== 'object' || depth > 24) return null
  const raw = value as Record<string, unknown>
  if (raw.kind === 'pane') {
    const id = raw.pane
    if (typeof id !== 'string' || !PANE_SET.has(id) || seen.has(id as WorkspacePaneID)) return null
    seen.add(id as WorkspacePaneID)
    return paneNode(id as WorkspacePaneID)
  }
  if (raw.kind !== 'split') return null
  const first = parseNode(raw.first, seen, depth + 1)
  const second = parseNode(raw.second, seen, depth + 1)
  if (!first) return second
  if (!second) return first
  return splitNode(raw.axis === 'vertical' ? 'vertical' : 'horizontal', Number(raw.ratio), first, second)
}

export function collectWorkspacePanes(node: WorkspaceLayoutNode): WorkspacePaneID[] {
  if (node.kind === 'pane') return [node.pane]
  return [...collectWorkspacePanes(node.first), ...collectWorkspacePanes(node.second)]
}

function finishLayout(root: WorkspaceLayoutNode): WorkspaceLayout {
  const open = collectWorkspacePanes(root)
  return {
    version: 2,
    root,
    open,
    order: [...open, ...DEFAULT_ORDER.filter((id) => !open.includes(id))],
  }
}

function buildLegacyTree(ids: WorkspacePaneID[], widths?: LegacyWorkspaceLayout['widths']): WorkspaceLayoutNode {
  if (ids.length === 1) return paneNode(ids[0])
  const weight = (id: WorkspacePaneID) => {
    const candidate = Number(widths?.[id])
    return Number.isFinite(candidate) && candidate > 0 ? candidate : DEFAULT_LEGACY_WIDTHS[id]
  }
  const firstWeight = weight(ids[0])
  const totalWeight = ids.reduce((sum, id) => sum + weight(id), 0)
  return splitNode('horizontal', firstWeight / totalWeight, paneNode(ids[0]), buildLegacyTree(ids.slice(1), widths))
}

function migrateLegacyLayout(raw: LegacyWorkspaceLayout): WorkspaceLayout {
  const suppliedOrder = paneList(raw.order)
  const order = [...suppliedOrder, ...DEFAULT_ORDER.filter((id) => !suppliedOrder.includes(id))]
  const requested = new Set(paneList(raw.open))
  requested.add('chat')
  const open = order.filter((id) => requested.has(id))
  return finishLayout(buildLegacyTree(open, raw.widths))
}

export function defaultWorkspaceLayout(): WorkspaceLayout {
  return finishLayout(splitNode('horizontal', 0.7, paneNode('chat'), paneNode('context')))
}

export function normalizeWorkspaceLayout(value: unknown): WorkspaceLayout {
  const raw = value && typeof value === 'object' ? value as Record<string, unknown> : null
  if (!raw) return defaultWorkspaceLayout()

  const parsed = parseNode(raw.root, new Set())
  if (!parsed) {
    if ('order' in raw || 'open' in raw || 'widths' in raw) return migrateLegacyLayout(raw as LegacyWorkspaceLayout)
    return defaultWorkspaceLayout()
  }

  const withChat = collectWorkspacePanes(parsed).includes('chat')
    ? parsed
    : splitNode('horizontal', 0.68, paneNode('chat'), parsed)
  return finishLayout(withChat)
}

function removePane(node: WorkspaceLayoutNode, id: WorkspacePaneID): WorkspaceLayoutNode | null {
  if (node.kind === 'pane') return node.pane === id ? null : node
  const first = removePane(node.first, id)
  const second = removePane(node.second, id)
  if (!first) return second
  if (!second) return first
  if (first === node.first && second === node.second) return node
  return splitNode(node.axis, node.ratio, first, second)
}

function insertRelative(
  node: WorkspaceLayoutNode,
  pane: WorkspacePaneID,
  target: WorkspacePaneID,
  edge: WorkspaceDropEdge,
): { node: WorkspaceLayoutNode; inserted: boolean } {
  if (node.kind === 'pane') {
    if (node.pane !== target) return { node, inserted: false }
    const incoming = paneNode(pane)
    const axis: WorkspaceSplitAxis = edge === 'top' || edge === 'bottom' ? 'vertical' : 'horizontal'
    const incomingFirst = edge === 'left' || edge === 'top'
    return {
      node: incomingFirst ? splitNode(axis, 0.5, incoming, node) : splitNode(axis, 0.5, node, incoming),
      inserted: true,
    }
  }
  const inFirst = insertRelative(node.first, pane, target, edge)
  if (inFirst.inserted) return { node: splitNode(node.axis, node.ratio, inFirst.node, node.second), inserted: true }
  const inSecond = insertRelative(node.second, pane, target, edge)
  if (inSecond.inserted) return { node: splitNode(node.axis, node.ratio, node.first, inSecond.node), inserted: true }
  return { node, inserted: false }
}

function defaultEdgeForPane(id: WorkspacePaneID): WorkspaceDropEdge {
  return id === 'terminal' ? 'bottom' : 'right'
}

function addPane(
  root: WorkspaceLayoutNode,
  id: WorkspacePaneID,
  target?: WorkspacePaneID,
  edge = defaultEdgeForPane(id),
): WorkspaceLayoutNode {
  const fallbackTarget = target && collectWorkspacePanes(root).includes(target) ? target : 'chat'
  const inserted = insertRelative(root, id, fallbackTarget, edge)
  if (inserted.inserted) return inserted.node
  const axis: WorkspaceSplitAxis = edge === 'top' || edge === 'bottom' ? 'vertical' : 'horizontal'
  const incoming = paneNode(id)
  return edge === 'left' || edge === 'top'
    ? splitNode(axis, 0.5, incoming, root)
    : splitNode(axis, 0.5, root, incoming)
}

export function setWorkspacePaneOpen(
  layout: WorkspaceLayout,
  id: WorkspacePaneID,
  open: boolean,
  target?: WorkspacePaneID,
  edge?: WorkspaceDropEdge,
): WorkspaceLayout {
  const current = normalizeWorkspaceLayout(layout)
  const isOpen = current.open.includes(id)
  if (open === isOpen || (id === 'chat' && !open)) return current
  if (!open) return finishLayout(removePane(current.root, id) || paneNode('chat'))
  return finishLayout(addPane(current.root, id, target, edge))
}

export function moveWorkspacePane(
  layout: WorkspaceLayout,
  source: WorkspacePaneID,
  target: WorkspacePaneID,
  edge: WorkspaceDropEdge,
): WorkspaceLayout {
  const current = normalizeWorkspaceLayout(layout)
  if (source === target || !current.open.includes(source) || !current.open.includes(target)) return current
  const withoutSource = removePane(current.root, source)
  if (!withoutSource) return current
  return finishLayout(addPane(withoutSource, source, target, edge))
}

function updateSplitRatio(node: WorkspaceLayoutNode, path: readonly number[], ratio: number): WorkspaceLayoutNode {
  if (node.kind !== 'split') return node
  if (path.length === 0) return splitNode(node.axis, ratio, node.first, node.second)
  const [head, ...rest] = path
  if (head === 0) return splitNode(node.axis, node.ratio, updateSplitRatio(node.first, rest, ratio), node.second)
  if (head === 1) return splitNode(node.axis, node.ratio, node.first, updateSplitRatio(node.second, rest, ratio))
  return node
}

export function resizeWorkspaceSplit(layout: WorkspaceLayout, path: readonly number[], ratio: number): WorkspaceLayout {
  const current = normalizeWorkspaceLayout(layout)
  return finishLayout(updateSplitRatio(current.root, path, clampWorkspaceSplitRatio(ratio)))
}

export function workspaceLayoutStorageKey(projectID: string, sessionID: string): string {
  return `gokin:workspace-layout:v2:${encodeURIComponent(projectID)}:${encodeURIComponent(sessionID)}`
}

export function legacyWorkspaceLayoutStorageKey(projectID: string, sessionID: string): string {
  return `gokin:workspace-layout:v1:${encodeURIComponent(projectID)}:${encodeURIComponent(sessionID)}`
}

export function readWorkspaceLayout(projectID: string, sessionID: string, storage: Pick<Storage, 'getItem'> = localStorage): WorkspaceLayout {
  try {
    const raw = storage.getItem(workspaceLayoutStorageKey(projectID, sessionID))
    if (raw) return normalizeWorkspaceLayout(JSON.parse(raw))
    const legacy = storage.getItem(legacyWorkspaceLayoutStorageKey(projectID, sessionID))
    return legacy ? normalizeWorkspaceLayout(JSON.parse(legacy)) : defaultWorkspaceLayout()
  } catch {
    return defaultWorkspaceLayout()
  }
}

export function writeWorkspaceLayout(projectID: string, sessionID: string, layout: WorkspaceLayout, storage: Pick<Storage, 'setItem'> = localStorage): void {
  try {
    storage.setItem(workspaceLayoutStorageKey(projectID, sessionID), JSON.stringify(normalizeWorkspaceLayout(layout)))
  } catch { /* private mode or storage quota */ }
}
