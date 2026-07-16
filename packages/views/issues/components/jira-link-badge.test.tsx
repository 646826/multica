// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { IssueJiraLink } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";

const linkRef = vi.hoisted(() => ({ current: null as IssueJiraLink | null }));

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: linkRef.current }),
}));

vi.mock("@multica/core/api", () => ({
  api: { getIssueJiraLink: vi.fn() },
}));

vi.mock("../../platform", () => ({
  openExternal: vi.fn(),
}));

import { JiraLinkBadge } from "./jira-link-badge";

function renderBadge() {
  return render(
    <I18nProvider locale="en" resources={{ en: { common: enCommon, issues: enIssues } }}>
      <JiraLinkBadge issueId="i1" />
    </I18nProvider>,
  );
}

describe("JiraLinkBadge", () => {
  beforeEach(() => {
    linkRef.current = null;
  });

  it("renders nothing for unlinked issues", () => {
    linkRef.current = { linked: false };
    renderBadge();
    expect(screen.queryByTestId("jira-link-badge")).toBeNull();
  });

  it("renders the key and a non-ok state chip", () => {
    linkRef.current = { linked: true, jira_key: "GAME-7", url: "https://x/browse/GAME-7", state: "retrying" };
    renderBadge();
    expect(screen.getByTestId("jira-link-badge").textContent).toContain("GAME-7");
    expect(screen.getByText("retrying")).toBeTruthy();
  });

  it("hides the chip when healthy", () => {
    linkRef.current = { linked: true, jira_key: "GAME-8", url: "https://x/browse/GAME-8", state: "ok" };
    renderBadge();
    expect(screen.queryByText("ok")).toBeNull();
  });
});
