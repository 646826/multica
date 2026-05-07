import { test, expect } from "@playwright/test";
import { loginAsDefault } from "./helpers";

/**
 * Phase 0 smoke tests for Benchmarks (suites + profiles).
 *
 * Covers the three primary flows:
 *   1. Sidebar → suites empty state.
 *   2. Create suite → land on detail → back to list.
 *   3. Capture profile (skipped at runtime if the seeded workspace has no agents,
 *      since Phase 0 doesn't provision agents itself).
 *
 * Selectors lean on i18n-stable text from `packages/views/locales/en/benchmarks.json`
 * and `packages/views/locales/en/layout.json`. The sidebar nav label "Benchmarks"
 * comes from `layout.json`.
 */
test.describe("Benchmarks — Phase 0 foundations", () => {
  let workspaceSlug: string;

  test.beforeEach(async ({ page }) => {
    workspaceSlug = await loginAsDefault(page);
  });

  test("sidebar entry lands on suites list with empty state", async ({ page }) => {
    // Click the "Benchmarks" sidebar link. The sidebar uses <nav> with anchor
    // tags labeled by i18n; matching by visible text is the established style
    // in `navigation.spec.ts`.
    await page.locator("nav a", { hasText: "Benchmarks" }).click();
    await page.waitForURL("**/benchmarks/suites", { timeout: 10000 });
    await expect(page).toHaveURL(
      new RegExp(`/${workspaceSlug}/benchmarks/suites$`),
    );

    // Sub-nav: "Suites" tab is active, "Runs" / "Leaderboard" disabled.
    await expect(
      page.getByRole("navigation", { name: /Benchmarks sections/i }),
    ).toBeVisible();

    // Empty state copy from benchmarks.json → suites_list.empty_title.
    await expect(page.getByRole("heading", { name: "No suites yet" })).toBeVisible();
  });

  test("can create a suite and see it in the list", async ({ page }) => {
    // Use a unique slug per run so the test stays idempotent against a
    // previously-populated DB (cleanup of suites isn't wired into fixtures).
    const uniqueSuffix = Date.now().toString(36);
    const slug = `smoke-cli-v1-${uniqueSuffix}`;
    const displayName = `Smoke CLI v1 ${uniqueSuffix}`;
    const instanceA = "abishekvashok__cmatrix.5c082c6";
    const instanceB = "psampaz__go-mod-outdated.bb79367";

    await page.goto(`/${workspaceSlug}/benchmarks/suites`);
    await page.waitForURL("**/benchmarks/suites");

    // CTA may render in either the empty state or the page header depending on
    // existing data — `.first()` keeps both cases working.
    await page.getByRole("button", { name: "Create suite" }).first().click();
    await page.waitForURL("**/benchmarks/suites/new");

    // Form fields use stable `htmlFor` ids and visible labels.
    await page.getByLabel("Slug").fill(slug);
    await page.getByLabel("Display name").fill(displayName);
    await page
      .getByLabel("Instance ids")
      .fill(`${instanceA}\n${instanceB}`);

    await page.getByRole("button", { name: "Create suite" }).click();

    // Suite detail URL is `/benchmarks/suites/{uuid}`.
    await page.waitForURL(/\/benchmarks\/suites\/[\w-]+$/, { timeout: 10000 });

    // Detail header shows the display name; instance ids appear in the list.
    await expect(
      page.getByRole("heading", { name: displayName }),
    ).toBeVisible();
    await expect(page.getByText(instanceA)).toBeVisible();
    await expect(page.getByText(instanceB)).toBeVisible();

    // Back arrow → list, and the new suite appears in the table.
    await page.getByRole("button", { name: "Back to suites" }).click();
    await page.waitForURL(/\/benchmarks\/suites$/);

    // Row contains both the slug (rendered as <code>) and the display name.
    await expect(page.getByText(slug)).toBeVisible();
    await expect(page.getByText(displayName)).toBeVisible();
  });

  test("capture profile flow", async ({ page }) => {
    const uniqueSuffix = Date.now().toString(36);
    const slug = `current-v1-${uniqueSuffix}`;
    const displayName = `Current v1 ${uniqueSuffix}`;

    await page.goto(`/${workspaceSlug}/benchmarks/profiles`);
    await page.waitForURL("**/benchmarks/profiles");

    // Click "Capture profile" CTA. If the workspace already has profiles, the
    // CTA is in the header bar; otherwise it sits in the empty state.
    await page.getByRole("button", { name: "Capture profile" }).first().click();
    await page.waitForURL("**/benchmarks/profiles/new");

    // Phase 0 fixtures don't seed agents into the e2e workspace. The capture
    // form requires a non-empty agent dropdown, so without an agent we can't
    // exercise the submit path. Detect that case and skip rather than fail —
    // this keeps the smoke test honest about what Phase 0 actually covers.
    const agentSelect = page.getByLabel("Agent");
    await expect(agentSelect).toBeVisible();

    // Pull all option values; the placeholder option has value="".
    const optionValues = await agentSelect.evaluate((el) =>
      Array.from((el as HTMLSelectElement).options)
        .map((o) => o.value)
        .filter((v) => v.length > 0),
    );

    test.skip(
      optionValues.length === 0,
      "No agents seeded in e2e workspace — Phase 0 doesn't provision agents, " +
        "so the capture submit path can't be exercised here. Add an agent " +
        "via the agents UI / API fixture to enable this case.",
    );

    // Pick the first real agent.
    await agentSelect.selectOption(optionValues[0]);

    await page.getByLabel("Slug").fill(slug);
    await page.getByLabel("Display name").fill(displayName);

    await page.getByRole("button", { name: "Capture profile" }).click();

    await page.waitForURL(/\/benchmarks\/profiles\/[\w-]+$/, {
      timeout: 10000,
    });

    await expect(
      page.getByRole("heading", { name: displayName }),
    ).toBeVisible();

    // Expand the prompt source <details>. The summary text comes from
    // benchmarks.json → profile_detail.prompt_show.
    const promptSummary = page.getByText("Show prompt source", { exact: true });
    await expect(promptSummary).toBeVisible();
    await promptSummary.click();

    // After expanding, the <pre> with the prompt source is rendered. We don't
    // assert on its content (depends on the agent's actual prompt) — verifying
    // the details element is open is enough for a smoke test.
    const detailsOpen = await page
      .locator("details", { has: promptSummary })
      .evaluate((el) => (el as HTMLDetailsElement).open);
    expect(detailsOpen).toBe(true);
  });
});
