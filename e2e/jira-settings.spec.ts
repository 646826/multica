import { test, expect } from "@playwright/test";
import { loginAsDefault } from "./helpers";

// Settings → Integrations → Jira connect flow (network-mocked, mirrors the
// Composio pattern in settings.spec.ts). Proves the section renders its gated
// states and drives a connect through the validated project picker.
test.describe("Jira integration settings", () => {
  test("connecting a Jira project shows a toast and the connected card", async ({
    page,
  }) => {
    const workspaceSlug = await loginAsDefault(page);
    const settingsUrl = `/${workspaceSlug}/settings?tab=integrations`;

    // Stateful: the connection endpoint returns "configured, no connection"
    // until the mocked connect lands, then the connected card.
    let connected = false;

    await page.route("**/api/workspaces/*/jira", (route) => {
      if (route.request().method() !== "GET") return route.continue();
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(
          connected
            ? {
                configured: true,
                can_manage: true,
                connection: {
                  id: "c1",
                  site_url: "https://acme.atlassian.net",
                  project_key: "GAME",
                  email: "bot@acme.test",
                  enabled: true,
                  mode: "jira_leads",
                  leading_system: "jira",
                  comments_enabled: true,
                  labels_enabled: true,
                  custom_fields_enabled: true,
                  create_from_jira: true,
                  create_to_jira: false,
                  jql_filter: "",
                  label_prefix: "",
                  mention_bridge_enabled: true,
                  outbound_issue_type: "Task",
                  cycle_interval_seconds: 45,
                  health: { state: "ok", last_cycle_at: "2026-07-16T12:00:00Z" },
                },
              }
            : { configured: true, can_manage: true, connection: null },
        ),
      });
    });

    await page.route("**/api/workspaces/*/jira/projects", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ projects: [{ id: "10001", key: "GAME", name: "Game Team" }] }),
      }),
    );

    await page.route("**/api/workspaces/*/jira/connect", (route) => {
      connected = true;
      route.fulfill({
        status: 201,
        contentType: "application/json",
        body: JSON.stringify({ id: "c1", project_key: "GAME" }),
      });
    });

    await page.goto(settingsUrl);
    await expect(page.getByText("Jira", { exact: true })).toBeVisible();

    // Fill credentials and validate → project picker appears.
    await page.getByLabel(/Jira site URL/i).fill("https://acme.atlassian.net");
    await page.getByLabel(/Service account email/i).fill("bot@acme.test");
    await page.getByLabel(/API token/i).fill("secret-token");
    await page.getByRole("button", { name: /Validate/i }).click();

    await expect(page.getByTestId("jira-project")).toBeVisible();
    await page.getByRole("button", { name: /^Connect$/i }).click();

    // Connected card shows the project + health.
    await expect(page.getByText(/GAME/)).toBeVisible();
    await expect(page.getByText(/Health/i)).toBeVisible();
  });
});
