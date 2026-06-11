import { test, expect } from '@playwright/test'
import { uniqueName, ensureLoggedIn, createRepo, createIssue } from './helpers'

/**
 * agent-sessions.spec (P0)
 *
 * Tests the Reset button on the agents tab of an issue detail page.
 * Covers AC #5 from architecture design:
 *   1. Failed session → Reset → confirm → success
 *   2. Running session → Reset → running-specific confirm dialog
 *   3. Cancel → no change
 *   4. 500 error → error message shown
 */

const PASSWORD = 'testpass123'

/** Build a minimal AgentSession object for the list response. */
function session(overrides: Record<string, unknown>) {
  return {
    session_id: 1,
    runner_id: 1,
    role_key: 'web',
    status: 'idle',
    repo_sha: 'abc12345',
    cause_kind: 'comment_mentioned',
    cause_id: '42',
    role_config: {
      triggers: ['comment_mentioned'],
      permission: 'write',
      tool_patterns: ['*'],
      model: 'gpt-4',
      container: { image: 'hangrix:latest' },
    },
    created_at: new Date().toISOString(),
    ended_at: null,
    ...overrides,
  }
}

test.describe('agent sessions reset', () => {
  let owner = ''
  const repoName = uniqueName('e2ereset')
  let issueNumber = 0

  test.beforeAll(async ({ browser }) => {
    const ctx = await browser.newContext()
    const page = await ctx.newPage()
    const username = uniqueName('e2eresetowner')
    owner = username
    await ensureLoggedIn(page, username, PASSWORD)

    await createRepo(page, repoName, {
      description: 'E2E agent session reset test',
      visibility: 'public',
    })

    issueNumber = await createIssue(
      page,
      owner,
      repoName,
      `E2E reset test ${uniqueName('')}`,
      'Testing reset button behaviour.',
    )

    await ctx.close()
  })

  test.beforeEach(async ({ page }) => {
    await ensureLoggedIn(page, owner, PASSWORD)
  })

  test('reset button shown on failed session, confirms, and calls API', async ({ page }) => {
    // Mock the sessions list: return one failed session.
    await page.route(
      `**/api/repos/${owner}/${repoName}/issues/${issueNumber}/agent-sessions`,
      async (route) => {
        if (route.request().method() === 'GET') {
          await route.fulfill({
            status: 200,
            contentType: 'application/json',
            body: JSON.stringify({
              items: [session({ session_id: 101, status: 'failed' })],
            }),
          })
        } else {
          await route.continue()
        }
      },
    )

    // Track whether the reset endpoint was called.
    let resetCalled = false
    await page.route(
      `**/api/repos/${owner}/${repoName}/issues/${issueNumber}/agent-sessions/101/reset`,
      async (route) => {
        if (route.request().method() === 'POST') {
          resetCalled = true
          await route.fulfill({
            status: 201,
            contentType: 'application/json',
            body: JSON.stringify({ new_session_id: 102 }),
          })
        } else {
          await route.continue()
        }
      },
    )

    // Navigate to the issue agents tab.
    await page.goto(`/${owner}/${repoName}/issues/${issueNumber}?tab=agents`)
    await page.getByRole('heading').first().waitFor({ state: 'visible', timeout: 15_000 })

    // Wait for the agents tab content to load — the session list should show.
    await expect(page.getByText('web')).toBeVisible({ timeout: 10_000 })
    await expect(page.getByText('failed')).toBeVisible({ timeout: 10_000 })

    // Click the session row to select it.
    await page.getByText('web').first().click()

    // The Reset button should be visible for failed status.
    const resetBtn = page.getByRole('button', { name: /Reset \(clear history\)/i })
    await expect(resetBtn).toBeVisible({ timeout: 5_000 })

    // Set up dialog listener: confirm the reset.
    page.once('dialog', (dialog) => {
      // Verify the dialog message is the non-running variant.
      expect(dialog.message()).toContain('archive')
      dialog.accept()
    })

    // Click Reset.
    await resetBtn.click()

    // Wait for the API call to complete (button should show "Resetting…" briefly).
    await expect(resetBtn).toBeEnabled({ timeout: 10_000 })
    // Verify the reset endpoint was called.
    expect(resetCalled).toBe(true)
  })

  test('reset on running session shows stricter confirm dialog', async ({ page }) => {
    // Mock the sessions list: return one running session.
    await page.route(
      `**/api/repos/${owner}/${repoName}/issues/${issueNumber}/agent-sessions`,
      async (route) => {
        if (route.request().method() === 'GET') {
          await route.fulfill({
            status: 200,
            contentType: 'application/json',
            body: JSON.stringify({
              items: [session({ session_id: 201, status: 'running' })],
            }),
          })
        } else {
          await route.continue()
        }
      },
    )

    // Navigate to the issue agents tab.
    await page.goto(`/${owner}/${repoName}/issues/${issueNumber}?tab=agents`)
    await page.getByRole('heading').first().waitFor({ state: 'visible', timeout: 15_000 })

    await expect(page.getByText('web')).toBeVisible({ timeout: 10_000 })
    await page.getByText('web').first().click()

    // The Reset button should be visible for running status.
    const resetBtn = page.getByRole('button', { name: /Reset \(clear history\)/i })
    await expect(resetBtn).toBeVisible({ timeout: 5_000 })

    // Verify the dialog shows the running-specific message.
    page.once('dialog', (dialog) => {
      expect(dialog.message()).toContain('still running')
      // Dismiss to avoid triggering the (unmocked for this test) reset endpoint.
      dialog.dismiss()
    })

    await resetBtn.click()
  })

  test('cancel reset dialog leaves session unchanged', async ({ page }) => {
    let resetCalled = false

    await page.route(
      `**/api/repos/${owner}/${repoName}/issues/${issueNumber}/agent-sessions`,
      async (route) => {
        if (route.request().method() === 'GET') {
          await route.fulfill({
            status: 200,
            contentType: 'application/json',
            body: JSON.stringify({
              items: [session({ session_id: 301, status: 'cancelled' })],
            }),
          })
        } else {
          await route.continue()
        }
      },
    )

    await page.route(
      `**/api/repos/${owner}/${repoName}/issues/${issueNumber}/agent-sessions/301/reset`,
      async (route) => {
        resetCalled = true
        await route.fulfill({ status: 201, body: '{}' })
      },
    )

    await page.goto(`/${owner}/${repoName}/issues/${issueNumber}?tab=agents`)
    await page.getByRole('heading').first().waitFor({ state: 'visible', timeout: 15_000 })

    await expect(page.getByText('web')).toBeVisible({ timeout: 10_000 })
    await page.getByText('web').first().click()

    const resetBtn = page.getByRole('button', { name: /Reset \(clear history\)/i })
    await expect(resetBtn).toBeVisible({ timeout: 5_000 })

    // Dismiss the confirm dialog.
    page.once('dialog', (dialog) => dialog.dismiss())

    await resetBtn.click()

    // The reset endpoint must NOT have been called.
    expect(resetCalled).toBe(false)

    // The session status badge should still show 'cancelled'.
    await expect(page.getByText('cancelled').first()).toBeVisible({ timeout: 5_000 })
  })

  test('500 error from reset endpoint shows error message', async ({ page }) => {
    await page.route(
      `**/api/repos/${owner}/${repoName}/issues/${issueNumber}/agent-sessions`,
      async (route) => {
        if (route.request().method() === 'GET') {
          await route.fulfill({
            status: 200,
            contentType: 'application/json',
            body: JSON.stringify({
              items: [session({ session_id: 401, status: 'failed' })],
            }),
          })
        } else {
          await route.continue()
        }
      },
    )

    // Return 500 with an error body.
    await page.route(
      `**/api/repos/${owner}/${repoName}/issues/${issueNumber}/agent-sessions/401/reset`,
      async (route) => {
        await route.fulfill({
          status: 500,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'internal error' }),
        })
      },
    )

    await page.goto(`/${owner}/${repoName}/issues/${issueNumber}?tab=agents`)
    await page.getByRole('heading').first().waitFor({ state: 'visible', timeout: 15_000 })

    await expect(page.getByText('web')).toBeVisible({ timeout: 10_000 })
    await page.getByText('web').first().click()

    const resetBtn = page.getByRole('button', { name: /Reset \(clear history\)/i })
    await expect(resetBtn).toBeVisible({ timeout: 5_000 })

    // Accept the confirm dialog.
    page.once('dialog', (dialog) => dialog.accept())

    await resetBtn.click()

    // The error message should appear (it's surfaced via the `error` ref in the component).
    await expect(page.getByText(/Reset failed/i)).toBeVisible({ timeout: 10_000 })
  })
})
