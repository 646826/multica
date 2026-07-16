import { describe, expect, it } from "vitest";
import { parseWithFallback } from "./schema";
import { JiraConnectionEnvelopeSchema } from "./schemas";
import type { JiraConnectionEnvelope } from "../types";

// Malformed-response guard for GET /api/workspaces/:id/jira (repo rule:
// every endpoint consumed by UI logic ships a schema + malformed test).

const FALLBACK: JiraConnectionEnvelope = {
  configured: false,
  can_manage: false,
  connection: null,
};

describe("JiraConnectionEnvelopeSchema", () => {
  it("parses a well-formed envelope with a connection", () => {
    const data = {
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
        mention_bridge_enabled: true,
        cycle_interval_seconds: 45,
        health: { state: "ok" },
        some_future_field: "ignored-but-tolerated",
      },
    };
    const parsed = parseWithFallback(data, JiraConnectionEnvelopeSchema, FALLBACK, {
      endpoint: "GET /api/workspaces/:id/jira",
    });
    expect(parsed.configured).toBe(true);
    expect(parsed.connection?.project_key).toBe("GAME");
  });

  it("parses the empty state (no connection yet)", () => {
    const parsed = parseWithFallback(
      { configured: true, can_manage: false, connection: null },
      JiraConnectionEnvelopeSchema,
      FALLBACK,
      { endpoint: "GET /api/workspaces/:id/jira" },
    );
    expect(parsed.connection).toBeNull();
    expect(parsed.can_manage).toBe(false);
  });

  it("falls back on a malformed response instead of throwing", () => {
    const malformed = { configured: "yes", connection: 42 };
    const parsed = parseWithFallback(malformed, JiraConnectionEnvelopeSchema, FALLBACK, {
      endpoint: "GET /api/workspaces/:id/jira",
    });
    expect(parsed).toEqual(FALLBACK);
  });

  it("tolerates unknown server-driven enum values (lenient by design)", () => {
    const data = {
      configured: true,
      can_manage: true,
      connection: {
        id: "c1",
        site_url: "https://acme.atlassian.net",
        project_key: "GAME",
        email: "bot@acme.test",
        enabled: true,
        mode: "hyperspace_mode",
        leading_system: "jira",
        comments_enabled: true,
        labels_enabled: true,
        custom_fields_enabled: true,
        create_from_jira: true,
        create_to_jira: false,
        mention_bridge_enabled: true,
        cycle_interval_seconds: 45,
      },
    };
    const parsed = parseWithFallback(data, JiraConnectionEnvelopeSchema, FALLBACK, {
      endpoint: "GET /api/workspaces/:id/jira",
    });
    expect(parsed.connection?.mode).toBe("hyperspace_mode");
  });
});
