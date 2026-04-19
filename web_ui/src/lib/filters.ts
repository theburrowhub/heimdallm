import type { Issue, IssueTriage, PR } from './types.js';

export interface PRFilter {
  repo: string; // "" = any
  severity: string; // "any" or a severity name
  state: string; // "open" | "closed" | "all"
}

export interface IssueFilter {
  repo: string; // "" = any
  severity: string; // "any" or a severity name
  mode: string; // "all" | "auto_implement" | "review_only"
}

export function filterPRs(prs: PR[], f: PRFilter): PR[] {
  return prs.filter((p) => {
    if (f.repo && p.repo !== f.repo) return false;
    if (f.state !== 'all' && p.state?.toLowerCase() !== f.state) return false;
    if (f.severity !== 'any') {
      const s = p.latest_review?.severity?.toLowerCase() ?? '';
      if (s !== f.severity) return false;
    }
    return true;
  });
}

export function filterIssues(issues: Issue[], f: IssueFilter): Issue[] {
  return issues.filter((i) => {
    if (i.dismissed) return false;
    if (f.repo && i.repo !== f.repo) return false;
    if (f.severity !== 'any') {
      const triage = i.latest_review?.triage as IssueTriage | undefined;
      const s = triage?.severity?.toLowerCase() ?? '';
      if (s !== f.severity) return false;
    }
    if (f.mode !== 'all') {
      if (!i.labels.includes(f.mode)) return false;
    }
    return true;
  });
}
