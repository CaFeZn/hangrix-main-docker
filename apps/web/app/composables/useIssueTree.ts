import { computed, ref } from 'vue'
import type { Issue } from '~/types/issue'

export interface TreeNode {
  issue: Issue
  depth: number        // 0 = root
  children: TreeNode[]
  descendantCount: number
}

/**
 * Build a tree from a flat list of issues using parent_number references.
 * The list is expected to be ordered: roots first (number DESC), then
 * descendants (depth ASC, number ASC). The tree preserves this order:
 * each root appears at depth 0 in the order it was found; children are
 * attached in their original list order under the parent.
 */
export function buildTree(items: Issue[]): TreeNode[] {
  const parentMap = new Map<number, Issue[]>()
  for (const iss of items) {
    const pn = iss.parent_number
    if (!parentMap.has(pn)) parentMap.set(pn, [])
    parentMap.get(pn)!.push(iss)
  }

  function attachChildren(parent: TreeNode) {
    const kids = parentMap.get(parent.issue.number) ?? []
    for (const kid of kids) {
      const child: TreeNode = { issue: kid, depth: parent.depth + 1, children: [], descendantCount: 0 }
      attachChildren(child)
      parent.children.push(child)
    }
    parent.descendantCount = parent.children.reduce((sum, c) => sum + 1 + c.descendantCount, 0)
  }

  const roots = parentMap.get(0) ?? []
  const tree: TreeNode[] = []
  for (const root of roots) {
    const node: TreeNode = { issue: root, depth: 0, children: [], descendantCount: 0 }
    attachChildren(node)
    tree.push(node)
  }
  return tree
}

/**
 * Flatten the tree into a list of { node, depth } for v-for rendering,
 * respecting collapsed state. A collapsed node hides all descendants.
 */
export function flattenVisible(tree: TreeNode[], collapsed: Set<number>): TreeNode[] {
  const result: TreeNode[] = []
  function walk(nodes: TreeNode[]) {
    for (const node of nodes) {
      result.push(node)
      if (!collapsed.has(node.issue.number)) {
        walk(node.children)
      }
    }
  }
  walk(tree)
  return result
}

/**
 * Composable that manages tree state for the issue list view.
 * Collapse state is session-only (lost on refresh), matching spec.
 */
export function useIssueTree() {
  const collapsed = ref(new Set<number>())

  function toggle(number: number) {
    const next = new Set(collapsed.value)
    if (next.has(number)) {
      next.delete(number)
    } else {
      next.add(number)
    }
    collapsed.value = next
  }

  function collapseAll(tree: TreeNode[]) {
    const next = new Set<number>()
    function collect(nodes: TreeNode[]) {
      for (const n of nodes) {
        if (n.children.length > 0) {
          next.add(n.issue.number)
          collect(n.children)
        }
      }
    }
    collect(tree)
    collapsed.value = next
  }

  function expandAll() {
    collapsed.value = new Set()
  }

  return { collapsed, toggle, collapseAll, expandAll }
}
