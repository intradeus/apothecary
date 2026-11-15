// A workshop's ticket source as the pages need it: whether tickets are
// written here (the internal tracker) and where the tracker lives for an
// "open in <tracker>" link.

import { INTERNAL_TRACKER, type Integration, type Project } from '$lib/domain';

/** The workshop's ticket-source integration; undefined while the list is
 *  loading or when the integration is gone. */
export const sourceOf = (
	project: Pick<Project, 'ticketSourceId'>,
	integrations: Integration[] | null | undefined
): Integration | undefined => (integrations ?? []).find((i) => i.id === project.ticketSourceId);

/** Tickets are written here only on the internal tracker; a remote tracker
 *  takes new issues itself. */
export const isInternal = (source: Pick<Integration, 'kind'> | undefined): boolean =>
	source?.kind === INTERNAL_TRACKER;

const str = (v: unknown) => (typeof v === 'string' ? v.trim().replace(/\/+$/, '') : '');

/** The single entry of a comma-separated scope list, '' when there are none
 *  or several (no one place to land then). */
function only(v: unknown): string {
	const list = str(v)
		.split(',')
		.map((s) => s.trim())
		.filter(Boolean);
	return list.length === 1 ? list[0] : '';
}

/** Where the tracker's issues are browsed, from its config: the site for
 *  Jira, the one project's or repository's issues on GitLab and GitHub (the
 *  host alone when the scope names several). '' when there is no telling. */
export function trackerHome(source: Pick<Integration, 'kind' | 'config'> | undefined): string {
	if (!source) return '';
	const base = str(source.config.base_url);
	switch (source.kind) {
		case 'jira':
			return base;
		case 'gitlab': {
			const site = base || 'https://gitlab.com';
			const p = only(source.config.projects);
			return p ? `${site}/${p}/-/issues` : site;
		}
		case 'github': {
			// base_url is the API: api.github.com, or <host>/api/v3 on Enterprise.
			const api = base || 'https://api.github.com';
			const site =
				api === 'https://api.github.com' ? 'https://github.com' : api.replace(/\/api\/v3$/, '');
			const r = only(source.config.repos);
			return r ? `${site}/${r}/issues` : site;
		}
		default:
			return '';
	}
}
