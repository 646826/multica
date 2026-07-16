"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ExternalLink } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { jiraConnectionOptions, jiraKeys } from "@multica/core/jira";
import type {
  JiraProject,
  JiraSyncMode,
  UpdateJiraConnectionPayload,
} from "@multica/core/types";
import { openExternal } from "../../platform";
import { useT } from "../../i18n";

// JiraSection is the Settings → Integrations panel for the native Jira sync
// Connection (one per workspace). Reading is member-visible; every management
// control is gated on the backend's can_manage hint (the router enforces it —
// the UI only mirrors). When the deployment lacks MULTICA_JIRA_SECRET_KEY the
// backend reports configured:false and this section shows the operator card.
export function JiraSection() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();

  const { data, isLoading } = useQuery(jiraConnectionOptions(wsId));
  const configured = data?.configured === true;
  const canManage = data?.can_manage === true;
  const connection = data?.connection ?? null;

  const [saving, setSaving] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const refresh = () => qc.invalidateQueries({ queryKey: jiraKeys.all(wsId) });

  const modeOptions = [
    { value: "mirror", label: t(($) => $.jira.mode_mirror) },
    { value: "jira_leads", label: t(($) => $.jira.mode_jira_leads) },
    { value: "multica_leads", label: t(($) => $.jira.mode_multica_leads) },
    { value: "two_way", label: t(($) => $.jira.mode_two_way) },
  ];
  const leadingOptions = [
    { value: "jira", label: t(($) => $.jira.leading_jira) },
    { value: "multica", label: t(($) => $.jira.leading_multica) },
  ];

  const patch = async (payload: UpdateJiraConnectionPayload) => {
    setSaving(true);
    try {
      await api.updateJiraConnection(wsId, payload);
      await refresh();
      toast.success(t(($) => $.jira.toast_saved));
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t(($) => $.jira.toast_save_failed));
    } finally {
      setSaving(false);
    }
  };

  if (isLoading) {
    return <p className="text-sm text-muted-foreground">{t(($) => $.jira.loading)}</p>;
  }

  if (!configured) {
    return (
      <Card>
        <CardContent className="space-y-1 py-4 text-sm">
          <p className="font-medium">{t(($) => $.jira.not_configured_title)}</p>
          <p className="text-muted-foreground">
            {t(($) => $.jira.not_configured_description_prefix)}{" "}
            <code className="rounded bg-muted px-1">MULTICA_JIRA_SECRET_KEY</code>{" "}
            {t(($) => $.jira.not_configured_description_suffix)}
          </p>
        </CardContent>
      </Card>
    );
  }

  if (!connection) {
    return canManage ? (
      <JiraConnectForm onConnected={refresh} />
    ) : (
      <p className="text-sm text-muted-foreground">{t(($) => $.jira.not_connected_member)}</p>
    );
  }

  const health = connection.health ?? {};
  const healthState = health.state ?? "unknown";

  return (
    <div className="space-y-4">
      <Card>
        <CardContent className="space-y-3 py-4">
          <div className="flex items-start justify-between gap-2">
            <div className="min-w-0">
              <p className="truncate text-sm font-medium">
                {connection.project_key} · {connection.site_url.replace(/^https:\/\//, "")}
              </p>
              <p className="text-xs text-muted-foreground">
                {t(($) => $.jira.health_label)}: {healthState}
                {health.last_cycle_at ? ` · ${t(($) => $.jira.health_last_cycle)} ${health.last_cycle_at}` : ""}
              </p>
              {health.last_error ? (
                <p className="truncate text-xs text-destructive">{health.last_error}</p>
              ) : null}
            </div>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => openExternal(`${connection.site_url}/browse/${connection.project_key}`)}
              title={t(($) => $.jira.open_in_jira)}
            >
              <ExternalLink className="size-4" />
            </Button>
          </div>

          <SettingSwitch
            label={t(($) => $.jira.enabled_label)}
            description={t(($) => $.jira.enabled_description)}
            checked={connection.enabled}
            disabled={!canManage || saving}
            onChange={(v) => patch({ enabled: v })}
          />

          <div className="flex items-center justify-between gap-2">
            <Label className="text-sm">{t(($) => $.jira.mode_label)}</Label>
            <Select
              items={modeOptions}
              value={connection.mode}
              onValueChange={(next) => {
                if (!next || next === connection.mode || !canManage || saving) return;
                void patch({ mode: next as JiraSyncMode });
              }}
            >
              <SelectTrigger className="w-56" size="sm" data-testid="jira-mode">
                <SelectValue>
                  {modeOptions.find((o) => o.value === connection.mode)?.label ?? connection.mode}
                </SelectValue>
              </SelectTrigger>
              <SelectContent align="end">
                {modeOptions.map((o) => (
                  <SelectItem key={o.value} value={o.value}>
                    {o.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {connection.mode === "two_way" ? (
            <div className="flex items-center justify-between gap-2">
              <Label className="text-sm">{t(($) => $.jira.leading_label)}</Label>
              <Select
                items={leadingOptions}
                value={connection.leading_system}
                onValueChange={(next) => {
                  if (!next || next === connection.leading_system || !canManage || saving) return;
                  void patch({ leading_system: next as "jira" | "multica" });
                }}
              >
                <SelectTrigger className="w-56" size="sm" data-testid="jira-leading">
                  <SelectValue>
                    {leadingOptions.find((o) => o.value === connection.leading_system)?.label ??
                      connection.leading_system}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent align="end">
                  {leadingOptions.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          ) : null}

          <SettingSwitch
            label={t(($) => $.jira.facet_comments)}
            checked={connection.comments_enabled}
            disabled={!canManage || saving}
            onChange={(v) => patch({ comments_enabled: v })}
          />
          <SettingSwitch
            label={t(($) => $.jira.facet_labels)}
            checked={connection.labels_enabled}
            disabled={!canManage || saving}
            onChange={(v) => patch({ labels_enabled: v })}
          />
          <SettingSwitch
            label={t(($) => $.jira.facet_custom_fields)}
            checked={connection.custom_fields_enabled}
            disabled={!canManage || saving}
            onChange={(v) => patch({ custom_fields_enabled: v })}
          />
          <SettingSwitch
            label={t(($) => $.jira.create_from_jira)}
            description={t(($) => $.jira.create_from_jira_description)}
            checked={connection.create_from_jira}
            disabled={!canManage || saving}
            onChange={(v) => patch({ create_from_jira: v })}
          />
          <SettingSwitch
            label={t(($) => $.jira.create_to_jira)}
            description={t(($) => $.jira.create_to_jira_description)}
            checked={connection.create_to_jira}
            disabled={!canManage || saving || connection.mode === "mirror"}
            onChange={(v) => patch({ create_to_jira: v })}
          />
          <SettingSwitch
            label={t(($) => $.jira.mention_bridge)}
            description={t(($) => $.jira.mention_bridge_description)}
            checked={connection.mention_bridge_enabled}
            disabled={!canManage || saving}
            onChange={(v) => patch({ mention_bridge_enabled: v })}
          />

          {canManage ? (
            <div className="flex justify-end pt-2">
              <Button
                variant="destructive"
                size="sm"
                disabled={saving}
                onClick={() => setConfirmDelete(true)}
              >
                {t(($) => $.jira.disconnect)}
              </Button>
            </div>
          ) : null}
        </CardContent>
      </Card>

      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.jira.disconnect_confirm_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.jira.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.jira.disconnect_confirm_cancel)}</AlertDialogCancel>
            <AlertDialogAction
              onClick={async () => {
                try {
                  await api.deleteJiraConnection(wsId);
                  await refresh();
                  toast.success(t(($) => $.jira.toast_disconnected));
                } catch (err) {
                  toast.error(err instanceof Error ? err.message : t(($) => $.jira.toast_disconnect_failed));
                }
              }}
            >
              {t(($) => $.jira.disconnect)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function SettingSwitch(props: {
  label: string;
  description?: string;
  checked: boolean;
  disabled: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <div className="flex items-center justify-between gap-2">
      <div className="min-w-0">
        <Label className="text-sm">{props.label}</Label>
        {props.description ? (
          <p className="text-xs text-muted-foreground">{props.description}</p>
        ) : null}
      </div>
      <Switch checked={props.checked} disabled={props.disabled} onCheckedChange={props.onChange} />
    </div>
  );
}

// JiraConnectForm walks the UJ-1 setup: credentials → live project list →
// mode preset → create. Nothing is persisted until the backend's live probe
// succeeds, so a failed validation leaves no half-configured state.
function JiraConnectForm({ onConnected }: { onConnected: () => void }) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();

  const [siteURL, setSiteURL] = useState("");
  const [email, setEmail] = useState("");
  const [token, setToken] = useState("");
  const [projects, setProjects] = useState<JiraProject[] | null>(null);
  const [projectKey, setProjectKey] = useState("");
  const [mode, setMode] = useState<JiraSyncMode>("jira_leads");
  const [busy, setBusy] = useState(false);

  const formModeOptions = [
    { value: "jira_leads", label: t(($) => $.jira.mode_jira_leads) },
    { value: "multica_leads", label: t(($) => $.jira.mode_multica_leads) },
    { value: "two_way", label: t(($) => $.jira.mode_two_way) },
    { value: "mirror", label: t(($) => $.jira.mode_mirror) },
  ];

  const loadProjects = async () => {
    setBusy(true);
    try {
      const resp = await api.listJiraProjects(wsId, { site_url: siteURL, email, token });
      setProjects(resp.projects);
      const first = resp.projects[0];
      if (first) setProjectKey(first.key);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t(($) => $.jira.toast_projects_failed));
    } finally {
      setBusy(false);
    }
  };

  const connect = async () => {
    setBusy(true);
    try {
      const project = projects?.find((p) => p.key === projectKey);
      await api.connectJira(wsId, {
        site_url: siteURL,
        email,
        token,
        project_key: projectKey,
        project_id: project?.id,
        mode,
      });
      toast.success(t(($) => $.jira.toast_connected));
      onConnected();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t(($) => $.jira.toast_connect_failed));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card>
      <CardContent className="space-y-3 py-4">
        <p className="text-sm text-muted-foreground">{t(($) => $.jira.page_description)}</p>
        <div className="space-y-2">
          <Label htmlFor="jira-site">{t(($) => $.jira.site_url_label)}</Label>
          <Input
            id="jira-site"
            placeholder="https://your-site.atlassian.net"
            value={siteURL}
            onChange={(e) => setSiteURL(e.target.value)}
          />
          <Label htmlFor="jira-email">{t(($) => $.jira.email_label)}</Label>
          <Input
            id="jira-email"
            placeholder="sync-bot@example.com"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
          <Label htmlFor="jira-token">{t(($) => $.jira.token_label)}</Label>
          <Input
            id="jira-token"
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
          />
          <p className="text-xs text-muted-foreground">{t(($) => $.jira.token_hint)}</p>
        </div>

        {projects === null ? (
          <Button
            size="sm"
            disabled={busy || !siteURL || !email || !token}
            onClick={loadProjects}
          >
            {busy ? t(($) => $.jira.validating) : t(($) => $.jira.load_projects)}
          </Button>
        ) : (
          <div className="space-y-3">
            <div className="flex items-center justify-between gap-2">
              <Label className="text-sm">{t(($) => $.jira.project_label)}</Label>
              <Select
                items={projects.map((p) => ({ value: p.key, label: `${p.key} — ${p.name}` }))}
                value={projectKey}
                onValueChange={(next) => {
                  if (next) setProjectKey(next);
                }}
              >
                <SelectTrigger className="w-56" size="sm" data-testid="jira-project">
                  <SelectValue>{projectKey}</SelectValue>
                </SelectTrigger>
                <SelectContent align="end">
                  {projects.map((p) => (
                    <SelectItem key={p.key} value={p.key}>
                      {p.key} — {p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex items-center justify-between gap-2">
              <Label className="text-sm">{t(($) => $.jira.mode_label)}</Label>
              <Select
                items={formModeOptions}
                value={mode}
                onValueChange={(next) => {
                  if (next) setMode(next as JiraSyncMode);
                }}
              >
                <SelectTrigger className="w-56" size="sm">
                  <SelectValue>
                    {formModeOptions.find((o) => o.value === mode)?.label ?? mode}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent align="end">
                  {formModeOptions.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex justify-end">
              <Button size="sm" disabled={busy || !projectKey} onClick={connect}>
                {busy ? t(($) => $.jira.connecting) : t(($) => $.jira.connect)}
              </Button>
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
