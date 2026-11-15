import { describe, expect, it } from 'vitest';
import { aProject } from '$test/fixtures';
import { anIntegration } from '$test/fixtures/app.svelte';
import { isInternal, sourceOf, trackerHome } from './tracker';

const tracker = (kind: string, config: Record<string, unknown> = {}) =>
	anIntegration({ port: 'ticketsource', kind, config });

describe('sourceOf', () => {
	it("finds the workshop's source among the integrations", () => {
		const src = anIntegration({ id: 'src' });
		expect(sourceOf(aProject({ ticketSourceId: 'src' }), [anIntegration(), src])).toBe(src);
	});

	it('is undefined while loading or when the integration is gone', () => {
		expect(sourceOf(aProject(), null)).toBeUndefined();
		expect(sourceOf(aProject(), [anIntegration({ id: 'other' })])).toBeUndefined();
	});
});

describe('isInternal', () => {
	it('is true only on the internal tracker', () => {
		expect(isInternal({ kind: 'internal' })).toBe(true);
		expect(isInternal({ kind: 'jira' })).toBe(false);
		expect(isInternal(undefined)).toBe(false);
	});
});

describe('trackerHome', () => {
	it.each([
		[
			'jira site',
			tracker('jira', { base_url: 'https://acme.atlassian.net/' }),
			'https://acme.atlassian.net'
		],
		['jira without a site', tracker('jira'), ''],
		['gitlab host', tracker('gitlab', { base_url: 'https://git.acme' }), 'https://git.acme'],
		[
			'gitlab, one project',
			tracker('gitlab', { projects: 'acme/app' }),
			'https://gitlab.com/acme/app/-/issues'
		],
		[
			'gitlab, several projects',
			tracker('gitlab', { base_url: 'https://git.acme', projects: 'a/b, c/d' }),
			'https://git.acme'
		],
		['github.com', tracker('github', { base_url: 'https://api.github.com' }), 'https://github.com'],
		[
			'github, one repo',
			tracker('github', { repos: ' acme/app ' }),
			'https://github.com/acme/app/issues'
		],
		[
			'github enterprise',
			tracker('github', { base_url: 'https://gh.acme/api/v3', repos: 'a/b' }),
			'https://gh.acme/a/b/issues'
		],
		['internal', tracker('internal'), ''],
		['no source', undefined, '']
	])('%s', (_, source, want) => {
		expect(trackerHome(source)).toBe(want);
	});
});
