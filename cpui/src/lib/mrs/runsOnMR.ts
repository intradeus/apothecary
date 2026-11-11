// A merge request's runs: those with it in their data — the one a run was
// started on (adopted), or one it opened. The engine finds them the same
// way (run.View.MRIIDs): a merge request belongs to no ticket.
import type { Run } from '$lib/domain';

const PRIMARY = 'repo';

/** The project's runs that went through merge request `iid` of checkout
 *  `repo` ('' or 'repo' = the primary), newest first. */
export function runsOnMR(runs: Run[], projectId: string, iid: number, repo = ''): Run[] {
	const want = repo || PRIMARY;
	return runs
		.filter(
			(r) =>
				r.projectId === projectId &&
				((r.adopted?.iid === iid && (r.adopted.repo || PRIMARY) === want) ||
					r.mrs.some((m) => m.iid === iid && (m.repo || PRIMARY) === want))
		)
		.sort((a, b) => b.createdAt.localeCompare(a.createdAt));
}
