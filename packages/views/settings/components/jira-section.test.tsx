// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { JiraConnectionEnvelope } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";

// The section reads exactly one query (the connection envelope) and renders
// three gated states: operator-setup (configured=false), connect form
// (admin, no connection), read-only note (member, no connection), and the
// connected management card. These tests pin the gating matrix; management
// mutations are backend-validated and covered by Go handler tests.

const envelopeRef = vi.hoisted(() => ({
  current: null as JiraConnectionEnvelope | null,
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: envelopeRef.current, isLoading: envelopeRef.current === null }),
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  queryOptions: <T,>(opts: T) => opts,
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/jira", () => ({
  jiraKeys: { all: (wsId: string) => ["jira", wsId] },
  jiraConnectionOptions: (wsId: string) => ({ queryKey: ["jira", wsId, "connection"] }),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    getJiraConnection: vi.fn(),
    connectJira: vi.fn(),
    updateJiraConnection: vi.fn(),
    deleteJiraConnection: vi.fn(),
    listJiraProjects: vi.fn(),
  },
}));

vi.mock("../../platform", () => ({
  openExternal: vi.fn(),
}));

import { JiraSection } from "./jira-section";

function renderSection() {
  return render(
    <I18nProvider locale="en" resources={{ en: { common: enCommon, settings: enSettings } }}>
      <JiraSection />
    </I18nProvider>,
  );
}

const connection = {
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
};

describe("Settings JiraSection", () => {
  beforeEach(() => {
    envelopeRef.current = null;
  });

  it("shows the operator card when the deployment key is unset", () => {
    envelopeRef.current = { configured: false, can_manage: true, connection: null };
    renderSection();
    expect(screen.getByText(/Jira integration not enabled/)).toBeTruthy();
    expect(screen.getByText(/MULTICA_JIRA_SECRET_KEY/)).toBeTruthy();
  });

  it("shows the connect form to admins when nothing is connected", () => {
    envelopeRef.current = { configured: true, can_manage: true, connection: null };
    renderSection();
    expect(screen.getByLabelText(/Jira site URL/)).toBeTruthy();
    expect(screen.getByLabelText(/API token/)).toBeTruthy();
  });

  it("shows a read-only note to members when nothing is connected", () => {
    envelopeRef.current = { configured: true, can_manage: false, connection: null };
    renderSection();
    expect(screen.getByText(/No Jira project is connected yet/)).toBeTruthy();
    expect(screen.queryByLabelText(/API token/)).toBeNull();
  });

  it("renders the connected card with health and no credential material", () => {
    envelopeRef.current = { configured: true, can_manage: true, connection };
    renderSection();
    expect(screen.getByText(/GAME · acme.atlassian.net/)).toBeTruthy();
    expect(screen.getByText(/Health: ok/)).toBeTruthy();
    expect(screen.getByText(/Disconnect/)).toBeTruthy();
    expect(document.body.textContent).not.toContain("bot@acme.test-token");
  });

  it("hides management controls from members on a connected project", () => {
    envelopeRef.current = { configured: true, can_manage: false, connection };
    renderSection();
    expect(screen.queryByText(/^Disconnect$/)).toBeNull();
  });
});
