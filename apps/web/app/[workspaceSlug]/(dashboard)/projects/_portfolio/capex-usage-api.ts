import { api } from "@multica/core/api";
import type { Issue, IssueMetadata } from "@multica/core/types";
import { usageMetadata, usageSchema, type CapexManual } from "./capex-usage";

async function allIssues(metadata?: IssueMetadata, projectId?: string): Promise<Issue[]> {
  const issues: Issue[] = [];
  for (let offset = 0; ; ) {
    const page = await api.listIssues({ metadata, project_id: projectId, limit: 100, offset });
    issues.push(...page.issues);
    if (page.issues.length < 100) return issues;
    offset += page.issues.length;
  }
}

export async function loadUsage(): Promise<CapexManual[]> {
  const issues = await allIssues({ capex_usage: true });
  const seen = new Set<string>();
  return issues.map((issue) => {
    const raw = issue.metadata.capex_usage_data;
    if (typeof raw !== "string") throw new Error("Forbrugspost mangler data");
    const entry = usageSchema.parse(JSON.parse(raw));
    if (entry.id !== issue.metadata.capex_usage_id || seen.has(entry.id)) throw new Error("Ugyldigt eller dubleret forbrugs-id");
    seen.add(entry.id);
    return { ...entry, issueId: issue.id };
  });
}

export async function saveUsage(entry: CapexManual): Promise<void> {
  const metadata = usageMetadata(entry);
  const projects = (await api.listProjects()).projects.filter((p) => p.title === "Multica-opsætning");
  const project = projects[0];
  if (projects.length !== 1 || !project) throw new Error("Projektet Multica-opsætning findes ikke entydigt");
  const title = `Capex-forbrug · ${entry.id}`;
  let matches = await allIssues({ capex_usage_id: entry.id });
  if (!matches.length) matches = (await allIssues(undefined, project.id)).filter((i) => i.title === title);
  if (matches.length > 1) throw new Error("Dubleret forbrugspost");
  const previous = matches[0];
  if (entry.issueId && previous?.id !== entry.issueId) throw new Error("Posten er ændret; genindlæs siden");
  const issue = previous ?? await api.createIssue({ title, project_id: project.id, status: "done" });
  // Allocation edits must retain a concurrently refreshed import snapshot.
  let data = metadata.capex_usage_data;
  if (previous && entry.issueId && typeof previous.metadata.capex_usage_data === "string") {
    const current = usageSchema.parse(JSON.parse(previous.metadata.capex_usage_data));
    const { capex_id: _capex, project_id: _project, ...rest } = current;
    data = usageMetadata({ ...rest, ...(entry.project_id ? { project_id: entry.project_id } : { capex_id: entry.capex_id ?? null }) }).capex_usage_data;
  }
  const combined = { ...issue.metadata, ...metadata, capex_usage_data: data };
  if (new TextEncoder().encode(JSON.stringify(combined)).length > 6000) throw new Error("Postens samlede metadata overstiger 6.000 bytes");
  if (previous && (issue.status !== "done" || issue.project_id !== project.id)) {
    await api.updateIssue(issue.id, { status: "done", project_id: project.id });
  }
  await api.setIssueMetadataKey(issue.id, "capex_usage_id", entry.id);
  await api.setIssueMetadataKey(issue.id, "capex_usage_data", data);
  await api.setIssueMetadataKey(issue.id, "capex_usage", true);
}

export async function removeUsage(entry: CapexManual): Promise<void> {
  if (!entry.issueId) throw new Error("Forbrugsposten mangler issue-id");
  const issue = await api.getIssue(entry.issueId);
  if (issue.metadata.capex_usage_id !== entry.id || issue.metadata.capex_usage !== true) throw new Error("Forbrugspostens id stemmer ikke");
  await api.deleteIssue(entry.issueId);
}
