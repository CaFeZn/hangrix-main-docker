/**
 * Detects and extracts the cross-post source-issue attribution that the
 * backend prepends to cross-issue comments.  The backend injects:
 *
 *   > This comment was cross-posted from issue #N
 *
 * as a markdown blockquote at the very start of the body.  This composable
 * strips it so the frontend can render a styled badge instead.
 */

const CROSS_POST_RE = /^> This comment was cross-posted from issue #(\d+)\n\n?/

export interface CrossPostInfo {
  sourceIssueNumber: number
  /** Body with the attribution blockquote stripped. */
  cleanBody: string
}

export function useCrossPostComment(body: string): CrossPostInfo | null {
  const m = body.match(CROSS_POST_RE)
  if (!m) return null

  const sourceIssueNumber = parseInt(m[1]!, 10)
  // Strip the matched prefix — the blockquote line and its trailing blank line.
  const cleanBody = body.slice(m[0].length)

  return { sourceIssueNumber, cleanBody }
}
