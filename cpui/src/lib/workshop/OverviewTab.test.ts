import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { Project, Run } from '$lib/domain';
import { aRun } from '$test/fixtures';
import { aFlow, anIntegration, aWorkshop } from '$test/fixtures/app.svelte';
import OverviewTab from './OverviewTab.svelte';

vi.mock('$lib/api', () => ({ api: {} }));

const project = (over: Partial<Project> = {}) =>
	aWorkshop({
		id: 'p1',
		repoAccountId: 'r',
		provisionerId: 'd',
		harnessIds: ['h'],
		defaultFlow: 'builder',
		...over
	});
const workspace = (over: Partial<Project['workspace']>) => ({
	workspace: { ...aWorkshop().workspace, ...over }
});
const integrations = [
	anIntegration({ id: 'r', name: 'Gitlab' }),
	anIntegration({ id: 'd', name: 'Docker' }),
	anIntegration({ id: 'h', name: 'Claude' })
];
const setup = (p: Project = project(), runs: Run[] = [], over: Record<string, unknown> = {}) =>
	render(OverviewTab, { project: p, integrations, flows: [], runs, ...over });
const pane = (heading: string) =>
	within(screen.getByRole('heading', { name: heading }).closest('section') as HTMLElement);

describe('OverviewTab, runs', () => {
	it('points to tickets with nothing live', () => {
		setup();
		expect(screen.getByRole('heading', { name: 'All still on the shelf' })).toBeInTheDocument();
		expect(screen.getByRole('link', { name: /New ticket/ })).toHaveAttribute(
			'href',
			'/tickets/new?project=p1'
		);
	});

	it('lists live runs newest first, with a count', () => {
		setup(project(), [
			aRun({ id: 'a', title: 'Older', updatedAt: '2026-10-01T00:00:00Z' }),
			aRun({ id: 'b', title: 'Newer', updatedAt: '2026-10-02T00:00:00Z' })
		]);
		const links = pane('Brewing now')
			.getAllByRole('link')
			.map((a) => a.textContent);
		expect(links.indexOf('Newer')).toBeLessThan(links.indexOf('Older'));
		expect(screen.queryByRole('link', { name: /New ticket/ })).not.toBeInTheDocument();
	});

	it('keeps four recently finished, leaving archived ones out', () => {
		const finished = [1, 2, 3, 4, 5].map((d) =>
			aRun({
				id: `f${d}`,
				title: `Done ${d}`,
				state: 'completed',
				updatedAt: `2026-10-0${d}T00:00:00Z`
			})
		);
		setup(project(), [...finished, aRun({ id: 'x', title: 'Shelved', state: 'archived' })]);

		expect(screen.getByText('Recently finished')).toBeInTheDocument();
		expect(screen.queryByRole('link', { name: 'Done 1' })).not.toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'Done 5' })).toBeInTheDocument();
		expect(screen.queryByRole('link', { name: 'Shelved' })).not.toBeInTheDocument();
	});
});

describe('OverviewTab, repositories', () => {
	it('shows the primary and links its section of the setup', () => {
		setup();
		const repos = pane('Repositories');
		expect(repos.getByText('https://git.test/dev/repo.git')).toBeInTheDocument();
		expect(repos.getByText('Gitlab')).toBeInTheDocument();
		expect(repos.getByRole('link', { name: 'Edit' })).toHaveAttribute(
			'href',
			'/w/p1/settings#repository'
		);
	});

	it('shows dots for an account it has not loaded', () => {
		setup(project(), [], { integrations: null });
		expect(pane('Repositories').getByText('…')).toBeInTheDocument();
	});

	it('counts the extras and marks a read-only one', () => {
		setup(
			project({
				repos: [
					{ name: 'api', url: 'https://x.git', locator: '', defaultBranch: 'main' },
					{ name: 'web', url: 'https://y.git', locator: '4', defaultBranch: 'main' }
				]
			})
		);
		const repos = pane('Repositories');
		expect(
			screen.getByRole('heading', { name: 'Repositories' }).nextElementSibling
		).toHaveTextContent('3');
		expect(repos.getAllByText('read-only')).toHaveLength(1);
	});
});

describe('OverviewTab, commands', () => {
	it('lists them alphabetically', () => {
		setup(project({ commands: { test: 'make test', build: 'make' } }));
		const names = pane('Commands')
			.getAllByRole('term')
			.map((d) => d.textContent?.trim());
		expect(names).toEqual(['build', 'test']);
	});

	it('treats no command name as special', () => {
		setup(project({ commands: { build: 'make', setup: 'npm ci' } }));
		const names = pane('Commands')
			.getAllByRole('term')
			.map((d) => d.textContent?.trim());
		expect(names).toEqual(['build', 'setup']);
		expect(pane('Commands').queryByText('bootstrap')).not.toBeInTheDocument();
		expect(pane('Commands').queryByText(/boots/)).not.toBeInTheDocument();
	});

	it('explains commands when there are none', () => {
		setup(project({ commands: {} }));
		expect(screen.getByRole('heading', { name: 'Commands' }).nextElementSibling).toHaveTextContent(
			'0'
		);
		expect(pane('Commands').getByText(/fail preflight/)).toBeInTheDocument();
		expect(pane('Commands').queryByText(/setup/)).not.toBeInTheDocument();
	});
});

describe('OverviewTab, the formula', () => {
	it('names and links the default flow', () => {
		setup(project(), [], { flows: [aFlow({ name: 'builder', hash: 'abcdef1234' })] });
		expect(screen.getByRole('link', { name: 'builder' })).toHaveAttribute(
			'href',
			'/w/p1/flows/builder'
		);
		expect(screen.getByText('@abcdef')).toBeInTheDocument();
	});

	it('marks a built-in or a copy of a template', () => {
		setup(project(), [], { flows: [aFlow({ name: 'builder', builtin: true })] });
		expect(pane('The formula').getByText('built-in')).toBeInTheDocument();
	});

	it('counts only flows of its own', () => {
		setup(project(), [], { flows: [aFlow({ name: 'a' }), aFlow({ name: 'b', builtin: true })] });
		expect(screen.getByRole('link', { name: /1 flow/ })).toHaveAttribute('href', '/w/p1?tab=flows');
	});

	it('shows a dash with no default', () => {
		setup(project({ defaultFlow: '' }));
		expect(pane('The formula').getByText('—')).toBeInTheDocument();
	});
});

describe('OverviewTab, egress', () => {
	it('reads an allowlist and its hosts', () => {
		setup(project(workspace({ extraDomains: ['a.test'], deniedDomains: ['b.test'] })));
		const egress = pane('Egress');
		expect(egress.getByText('Allowlist')).toHaveClass('tint-success');
		expect(egress.getByText('a.test')).toBeInTheDocument();
		expect(egress.queryByText('b.test')).not.toBeInTheDocument();
	});

	it('reads a denylist and its hosts', () => {
		setup(
			project(
				workspace({ egressMode: 'deny', extraDomains: ['a.test'], deniedDomains: ['b.test'] })
			)
		);
		const egress = pane('Egress');
		expect(egress.getByText('Denylist')).toHaveClass('tint-warning');
		expect(egress.getByText('b.test')).toHaveClass('tint-danger');
		expect(egress.queryByText('a.test')).not.toBeInTheDocument();
	});

	it('says when the list is empty, by mode', () => {
		const { unmount } = setup();
		expect(screen.getByText(/No extra hosts/)).toBeInTheDocument();
		unmount();
		setup(project(workspace({ egressMode: 'deny' })));
		expect(screen.getByText('Nothing blocked.')).toBeInTheDocument();
	});

	it('caps a long list and unfolds it', async () => {
		const extraDomains = Array.from({ length: 20 }, (_, i) => `h${i}.test`);
		setup(project(workspace({ extraDomains })));
		expect(screen.queryByText('h19.test')).not.toBeInTheDocument();

		await userEvent.click(screen.getByRole('button', { name: '+2 more' }));
		expect(screen.getByText('h19.test')).toBeInTheDocument();

		await userEvent.click(screen.getByRole('button', { name: 'Show less' }));
		expect(screen.queryByText('h19.test')).not.toBeInTheDocument();
	});

	it('does not offer to unfold a list that fits', () => {
		setup(project(workspace({ extraDomains: Array.from({ length: 18 }, (_, i) => `h${i}.test`) })));
		expect(screen.getByText('h17.test')).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: /more/ })).not.toBeInTheDocument();
	});

	it('shows force-blocked hosts', () => {
		setup(project(workspace({ blockedDomains: ['evil.test'] })));
		expect(screen.getByText(/Force-blocked/)).toBeInTheDocument();
		expect(screen.getByText('evil.test')).toBeInTheDocument();
	});
});

describe('OverviewTab, workspace', () => {
	it('names the provisioner and harnesses', () => {
		setup();
		const ws = pane('Workspace');
		expect(ws.getByText('Docker')).toBeInTheDocument();
		expect(ws.getByText('Claude')).toBeInTheDocument();
	});

	it('sums up the editor, or says it is on defaults', () => {
		const { unmount } = setup();
		expect(screen.getByText('code-server defaults')).toBeInTheDocument();
		unmount();

		setup(
			project(
				workspace({
					editor: {
						theme: 'Monokai',
						fontSize: 14,
						tabSize: 2,
						formatOnSave: true,
						wordWrap: true,
						trustWorkspace: true
					}
				})
			)
		);
		expect(
			screen.getByText('Monokai · font 14 · tab 2 · format on save · word wrap · trusted')
		).toBeInTheDocument();
	});

	it('lists extensions, or a dash', () => {
		setup(project(workspace({ codeServerExtensions: ['a.b', 'c.d'] })));
		expect(screen.getByText('a.b, c.d')).toBeInTheDocument();
	});
});
