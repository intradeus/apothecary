import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { replaceState } from '$app/navigation';
import { api } from '$lib/api';
import type { Project, ProjectDraft } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { aFlow, anIntegration, aWorkshop, choose } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import ProjectForm from './ProjectForm.svelte';

vi.mock('$lib/api', () => ({
	api: {
		projects: {
			create: vi.fn(),
			update: vi.fn(),
			startEnvCheck: vi.fn(),
			browser: vi.fn(),
			secrets: vi.fn()
		},
		integrations: { repos: vi.fn() },
		egress: { defaults: vi.fn() }
	}
}));

const integrations = [
	anIntegration({ id: 'repo-1', name: 'Gitlab', port: 'repository', kind: 'gitlab' }),
	anIntegration({ id: 'repo-2', name: 'Github', port: 'repository', kind: 'github' }),
	anIntegration({ id: 'prov-1', name: 'Docker', port: 'provisioner', kind: 'docker' }),
	anIntegration({ id: 'prov-2', name: 'K8s', port: 'provisioner', kind: 'kubernetes' }),
	anIntegration({ id: 'h-a', name: 'Claude A', port: 'harness', kind: 'claude' }),
	anIntegration({ id: 'h-b', name: 'Claude B', port: 'harness', kind: 'claude' }),
	anIntegration({ id: 'h-c', name: 'Codex', port: 'harness', kind: 'codex' })
];
const flows = [aFlow({ name: 'mine' }), aFlow({ name: 'tpl', builtin: true })];

const stored = (over: Partial<Project> = {}) =>
	aWorkshop({
		id: 'p1',
		name: 'billing',
		repoAccountId: 'repo-1',
		provisionerId: 'prov-1',
		harnessIds: ['h-a'],
		repo: {
			url: 'https://git.test/a.git',
			locator: '7',
			defaultBranch: 'trunk',
			agentsMdPath: 'A.md'
		},
		repos: [],
		commands: { test: 'make test' },
		defaultFlow: 'mine',
		...over
	});

function setup(over: Record<string, unknown> = {}) {
	const onsaved = vi.fn();
	const oncancel = vi.fn();
	const view = render(ProjectForm, { integrations, flows, onsaved, oncancel, ...over });
	return { onsaved, oncancel, ...view };
}
const edit = (over: Partial<Project> = {}) => setup({ existing: stored(over) });

const saveBtn = () => screen.getByRole('button', { name: 'Save setup' });
const createBtn = () => screen.getByRole('button', { name: 'Create workshop' });
const envcheck = () => screen.getByRole('switch', { name: 'Run envcheck after saving' });
const updated = () => vi.mocked(api.projects.update).mock.calls.at(-1)![1];
const created = () => vi.mocked(api.projects.create).mock.calls.at(-1)![0];
/** Saves the edit and returns the draft the engine got. */
async function save(): Promise<ProjectDraft> {
	await userEvent.click(saveBtn());
	return updated();
}
const retype = async (label: string, value: string) => {
	const field = screen.getByLabelText(label);
	await userEvent.clear(field);
	if (value) await userEvent.type(field, value);
};
const step = (name: RegExp) =>
	within(screen.getByRole('navigation', { name: 'Setup steps' })).getByRole('button', { name });

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.projects.create).mockImplementation(async (d) => stored({ name: d.name }));
	vi.mocked(api.projects.update).mockImplementation(async (id, d) => stored({ id, name: d.name }));
	vi.mocked(api.projects.browser).mockResolvedValue({
		enabled: false,
		available: true,
		state: 'absent',
		sessions: 0
	});
	vi.mocked(api.projects.secrets).mockResolvedValue([]);
	toasts.list = [];
	history.replaceState(null, '', '/w/p1/settings');
	// The router's, down to what these tests read: the address.
	vi.mocked(replaceState).mockImplementation((url) => history.replaceState(null, '', url));
});

describe('ProjectForm, draft()', () => {
	it('sends back exactly what it was given when nothing was edited', async () => {
		edit({
			repos: [{ name: 'api', url: 'https://x.git', locator: '9', defaultBranch: 'dev' }],
			workspace: {
				egressMode: 'deny',
				extraDomains: ['a.test'],
				deniedDomains: ['b.test'],
				blockedDomains: ['evil.test'],
				allowedCidrs: ['10.0.0.0/8'],
				allowedPorts: [5432],
				codeServerExtensions: ['golang.go'],
				editor: {
					theme: 'Monokai',
					fontSize: 14,
					tabSize: 2,
					formatOnSave: true,
					wordWrap: false,
					trustWorkspace: true
				},
				services: [{ name: 'web', start: 'go', ports: [] }],
				browserEnabled: true
			}
		} as Partial<Project>);
		await userEvent.click(envcheck());

		expect(await save()).toEqual({
			name: 'billing',
			repoAccountId: 'repo-1',
			provisionerId: 'prov-1',
			harnessIds: ['h-a'],
			repo: {
				url: 'https://git.test/a.git',
				locator: '7',
				defaultBranch: 'trunk',
				agentsMdPath: 'A.md'
			},
			repos: [{ name: 'api', url: 'https://x.git', locator: '9', defaultBranch: 'dev' }],
			commands: { test: 'make test' },
			workspace: {
				egressMode: 'deny',
				extraDomains: ['a.test'],
				deniedDomains: ['b.test'],
				blockedDomains: ['evil.test'],
				allowedCidrs: ['10.0.0.0/8'],
				allowedPorts: [5432],
				codeServerExtensions: ['golang.go'],
				editor: {
					theme: 'Monokai',
					fontSize: 14,
					tabSize: 2,
					formatOnSave: true,
					wordWrap: false,
					trustWorkspace: true
				},
				services: [
					{
						name: 'web',
						description: undefined,
						start: 'go',
						stop: undefined,
						health: undefined,
						ports: []
					}
				],
				browserEnabled: true
			},
			defaultFlow: 'mine'
		});
	});

	it('trims what is typed', async () => {
		edit();
		await retype('Workshop name', '  acme ');
		await retype('Repo URL', ' https://new.git ');
		await retype('Locator', ' 8 ');
		await retype('Default branch', ' dev ');
		await retype('AGENTS.md path', ' B.md ');

		expect(await save()).toMatchObject({
			name: 'acme',
			repo: { url: 'https://new.git', locator: '8', defaultBranch: 'dev', agentsMdPath: 'B.md' }
		});
	});

	it('leaves out commands with no name or no script, keeping scripts as written', async () => {
		edit({ commands: { test: '  make test\n', blank: '   ' } });
		await userEvent.click(screen.getByRole('button', { name: /Add command/ }));
		const keys = screen.getAllByRole('textbox', { name: 'Key' });
		await userEvent.type(screen.getAllByRole('textbox', { name: /^Value for/ }).at(-1)!, 'orphan');
		await userEvent.clear(keys[0]);
		await userEvent.type(keys[0], ' test ');
		await userEvent.click(envcheck());

		expect((await save()).commands).toEqual({ test: '  make test\n' });
	});

	it('reads empty editor numbers as zero', async () => {
		edit({
			workspace: { ...stored().workspace, editor: { fontSize: 14 } as never }
		});
		await retype('Font size', '');
		expect((await save()).workspace.editor).toMatchObject({ fontSize: 0, tabSize: 0 });
	});

	it('cleans the extensions', async () => {
		edit({
			workspace: { ...stored().workspace, codeServerExtensions: [' a.b ', '  ', 'c.d'] }
		});
		await userEvent.click(envcheck());
		expect((await save()).workspace.codeServerExtensions).toEqual(['a.b', 'c.d']);
	});

	it('cleans extra repositories: branch defaults to main, no empty locator', async () => {
		edit();
		await userEvent.click(screen.getByRole('button', { name: /Add repository/ }));
		await userEvent.type(screen.getByLabelText('checkout name'), ' api ');
		await userEvent.type(screen.getByLabelText('repo url'), ' https://x.git ');
		await userEvent.clear(screen.getByLabelText('default branch'));

		expect((await save()).repos).toEqual([
			{ name: 'api', url: 'https://x.git', locator: undefined, defaultBranch: 'main' }
		]);
	});

	it('keeps one harness per kind, in selection order', async () => {
		edit({ harnessIds: ['h-c', 'h-a', 'h-b'] });
		await userEvent.click(envcheck());
		expect((await save()).harnessIds).toEqual(['h-c', 'h-a']);
	});

	it('drops a harness that no longer exists', async () => {
		edit({ harnessIds: ['gone', 'h-a'] });
		await userEvent.click(envcheck());
		expect((await save()).harnessIds).toEqual(['h-a']);
	});

	it('survives a project with no workspace policy set', async () => {
		edit({
			workspace: {
				egressMode: 'allow',
				services: [],
				browserEnabled: false
			} as unknown as Project['workspace']
		});
		await userEvent.click(envcheck());
		expect((await save()).workspace).toMatchObject({
			extraDomains: [],
			deniedDomains: [],
			blockedDomains: [],
			allowedCidrs: [],
			allowedPorts: []
		});
	});
});

describe('ProjectForm, every section reaches the draft', () => {
	it('repository: account, URL, locator, branch, guide', async () => {
		edit();
		await choose('Repository account', 'Github (github)');
		await retype('Repo URL', 'https://b.git');
		await retype('Locator', '8');
		await retype('Default branch', 'dev');
		await retype('AGENTS.md path', 'B.md');

		expect(await save()).toMatchObject({
			repoAccountId: 'repo-2',
			repo: { url: 'https://b.git', locator: '8', defaultBranch: 'dev', agentsMdPath: 'B.md' }
		});
	});

	it('repository: a repository picked in the browser', async () => {
		vi.mocked(api.integrations.repos).mockResolvedValue([
			{ name: 'acme/new', url: 'https://new.git', locator: '99', defaultBranch: 'next' } as never
		]);
		edit();
		await userEvent.click(screen.getByRole('button', { name: 'Browse repositories' }));
		await userEvent.click(await screen.findByRole('button', { name: 'Use' }));

		expect(await save()).toMatchObject({
			name: 'billing',
			repo: { url: 'https://new.git', locator: '99', defaultBranch: 'next' }
		});
	});

	it('repository: an empty name is filled from the pick, and shows in the Workshop section', async () => {
		vi.mocked(api.integrations.repos).mockResolvedValue([
			{ name: 'acme/new', url: 'https://new.git', locator: '99', defaultBranch: 'next' } as never
		]);
		setup();
		await userEvent.click(screen.getByRole('button', { name: 'Browse repositories' }));
		await userEvent.click(await screen.findByRole('button', { name: 'Use' }));
		expect(screen.getByLabelText('Workshop name')).toHaveValue('new');
	});

	it('repository: extra repositories', async () => {
		edit({ repos: [{ name: 'api', url: 'https://x.git', locator: '', defaultBranch: 'main' }] });
		await userEvent.type(screen.getByLabelText('locator (empty = read-only)'), '5');
		expect((await save()).repos[0]).toMatchObject({ name: 'api', locator: '5' });
	});

	it('repository: an extra repository removed', async () => {
		edit({
			repos: [
				{ name: 'api', url: 'https://x.git', locator: '', defaultBranch: 'main' },
				{ name: 'web', url: 'https://y.git', locator: '', defaultBranch: 'main' }
			]
		});
		await userEvent.click(screen.getByRole('button', { name: 'Remove api' }));
		expect((await save()).repos.map((r) => r.name)).toEqual(['web']);
	});

	it('workshop: name, default flow, commands', async () => {
		edit();
		await retype('Workshop name', 'acme');
		await choose('Default flow', 'tpl');
		await userEvent.type(screen.getByRole('textbox', { name: 'Value for test' }), ' -v');

		expect(await save()).toMatchObject({
			name: 'acme',
			defaultFlow: 'tpl',
			commands: { test: 'make test -v' }
		});
	});

	it('execution: provisioner and harnesses', async () => {
		edit();
		await choose('Provisioner', 'K8s (kubernetes)');
		await userEvent.click(screen.getByRole('checkbox', { name: /Codex/ }));

		expect(await save()).toMatchObject({ provisionerId: 'prov-2', harnessIds: ['h-a', 'h-c'] });
	});

	it('workspace: the egress policy, both lists kept', async () => {
		edit({
			workspace: { ...stored().workspace, extraDomains: ['a.test'], deniedDomains: ['b.test'] }
		});
		await userEvent.type(
			screen.getByPlaceholderText('api.example.com, *.example.com'),
			'c.test{Enter}'
		);
		await userEvent.click(screen.getByRole('radio', { name: 'Denylist' }));
		await userEvent.type(
			screen.getByPlaceholderText('tracker.example.com, *.ads.net'),
			'd.test{Enter}'
		);

		expect((await save()).workspace).toMatchObject({
			egressMode: 'deny',
			extraDomains: ['a.test', 'c.test'],
			deniedDomains: ['b.test', 'd.test']
		});
	});

	it('workspace: the defaults added from the engine', async () => {
		vi.mocked(api.egress.defaults).mockResolvedValue(['pypi.org']);
		edit();
		await userEvent.click(screen.getByRole('button', { name: /Add sensible defaults/ }));
		await screen.findByText('+1 added');
		expect((await save()).workspace.extraDomains).toEqual(['pypi.org']);
	});

	it('workspace: extensions and the editor', async () => {
		edit();
		await userEvent.type(
			screen.getByPlaceholderText('golang.go, ms-python.python'),
			'golang.go{Enter}'
		);
		await choose('Color theme', 'Monokai');
		await retype('Font size', '13');
		await retype('Tab size', '4');
		await userEvent.click(screen.getByRole('switch', { name: 'Format on save' }));
		await userEvent.click(screen.getByRole('switch', { name: 'Word wrap' }));
		await userEvent.click(screen.getByRole('switch', { name: 'Trust workspace' }));

		expect((await save()).workspace).toMatchObject({
			codeServerExtensions: ['golang.go'],
			editor: {
				theme: 'Monokai',
				fontSize: 13,
				tabSize: 4,
				formatOnSave: true,
				wordWrap: true,
				trustWorkspace: true
			}
		});
	});

	it('services: a new one, with a probe and a port', async () => {
		edit();
		await userEvent.click(screen.getByRole('button', { name: /Add a service/ }));
		await userEvent.type(screen.getByLabelText('Start'), 'npm start');
		await userEvent.click(screen.getByRole('radio', { name: 'HTTP' }));
		await userEvent.type(screen.getByLabelText('URL'), 'http://localhost:3000');
		await userEvent.click(screen.getByRole('button', { name: /Add port/ }));
		await userEvent.type(screen.getByRole('spinbutton', { name: 'port' }), '3000');

		expect((await save()).workspace.services).toEqual([
			{
				name: 'default',
				description: undefined,
				start: 'npm start',
				stop: undefined,
				health: { http: 'http://localhost:3000', timeout: undefined, interval: undefined },
				ports: [{ port: 3000, label: undefined, path: undefined }]
			}
		]);
	});

	it('browser: the toggle', async () => {
		edit();
		await userEvent.click(screen.getByRole('switch', { name: 'Give this workshop a browser' }));
		expect((await save()).workspace.browserEnabled).toBe(true);
	});
});

describe('ProjectForm, dirty and save', () => {
	it('waits for a change before an edit can save', async () => {
		edit();
		expect(saveBtn()).toBeDisabled();
		expect(screen.queryByText('Unsaved changes')).not.toBeInTheDocument();

		await userEvent.type(screen.getByLabelText('Workshop name'), 'x');
		expect(saveBtn()).toBeEnabled();
		expect(screen.getByText('Unsaved changes')).toBeInTheDocument();
	});

	it('goes clean again when an edit is undone', async () => {
		edit();
		await userEvent.type(screen.getByLabelText('Workshop name'), 'x');
		await userEvent.type(screen.getByLabelText('Workshop name'), '{Backspace}');
		expect(saveBtn()).toBeDisabled();
	});

	it('takes the envcheck toggle as a reason to save', async () => {
		edit();
		await userEvent.click(envcheck());
		expect(saveBtn()).toBeEnabled();
	});

	it('is always ready to create', () => {
		setup();
		expect(createBtn()).toBeEnabled();
	});

	it('updates an existing workshop, not creating one', async () => {
		const { onsaved } = edit();
		await retype('Workshop name', 'acme');
		await save();

		expect(api.projects.update).toHaveBeenCalledWith('p1', expect.anything());
		expect(api.projects.create).not.toHaveBeenCalled();
		await waitFor(() => expect(onsaved).toHaveBeenCalledOnce());
		expect(onsaved.mock.calls[0][1]).toBe(false);
		expect(toasts.list.at(-1)?.text).toBe('acme setup saved');
	});

	it('holds Save while saving', async () => {
		const pending = deferred<Project>();
		vi.mocked(api.projects.update).mockReturnValue(pending.promise);
		edit();
		await retype('Workshop name', 'acme');
		await userEvent.click(saveBtn());

		expect(saveBtn()).toBeDisabled();
		expect(saveBtn()).toHaveAttribute('aria-busy', 'true');
		await userEvent.type(screen.getByLabelText('Workshop name'), '{Enter}');
		expect(api.projects.update).toHaveBeenCalledOnce();

		pending.resolve(stored());
		await waitFor(() => expect(saveBtn()).toBeEnabled());
	});

	it('shows a refusal and stays, keeping the edits', async () => {
		vi.mocked(api.projects.update).mockRejectedValue(new Error('name taken'));
		const { onsaved } = edit();
		await retype('Workshop name', 'acme');
		await userEvent.click(saveBtn());

		expect(await screen.findByText('Couldn’t save this setup')).toBeInTheDocument();
		expect(screen.getByText('name taken')).toBeInTheDocument();
		expect(onsaved).not.toHaveBeenCalled();
		expect(screen.getByLabelText('Workshop name')).toHaveValue('acme');
	});

	it('clears the error on the next try', async () => {
		vi.mocked(api.projects.update).mockRejectedValueOnce(new Error('name taken'));
		edit();
		await retype('Workshop name', 'acme');
		await userEvent.click(saveBtn());
		await screen.findByText('name taken');

		await userEvent.click(saveBtn());
		await waitFor(() => expect(screen.queryByText('name taken')).not.toBeInTheDocument());
	});

	it('cancels', async () => {
		const { oncancel } = edit();
		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		expect(oncancel).toHaveBeenCalledOnce();
		expect(api.projects.update).not.toHaveBeenCalled();
	});
});

describe('ProjectForm, envcheck after saving', () => {
	it('starts one and tells the page', async () => {
		vi.mocked(api.projects.startEnvCheck).mockResolvedValue('check-1');
		const { onsaved } = edit();
		await userEvent.click(envcheck());
		await userEvent.click(saveBtn());

		await waitFor(() => expect(onsaved).toHaveBeenCalledOnce());
		expect(api.projects.startEnvCheck).toHaveBeenCalledWith('p1', false);
		expect(onsaved.mock.calls[0][1]).toBe(true);
	});

	it('saved anyway when the check fails to start', async () => {
		vi.mocked(api.projects.startEnvCheck).mockRejectedValue(new Error('no capacity'));
		const { onsaved } = edit();
		await userEvent.click(envcheck());
		await userEvent.click(saveBtn());

		await waitFor(() => expect(onsaved).toHaveBeenCalledOnce());
		expect(onsaved.mock.calls[0][1]).toBe(false);
		expect(screen.queryByText('Couldn’t save this setup')).not.toBeInTheDocument();
	});

	it('starts nothing unless asked', async () => {
		const { onsaved } = edit();
		await retype('Workshop name', 'acme');
		await userEvent.click(saveBtn());
		await waitFor(() => expect(onsaved).toHaveBeenCalled());
		expect(api.projects.startEnvCheck).not.toHaveBeenCalled();
	});
});

describe('ProjectForm, creating', () => {
	async function fill() {
		await userEvent.type(screen.getByLabelText('Repo URL'), 'https://a.git');
		await userEvent.type(screen.getByLabelText('Locator'), '1');
		await userEvent.type(screen.getByLabelText('Workshop name'), 'acme');
	}

	it('starts from sensible picks', async () => {
		setup();
		await fill();
		await userEvent.click(createBtn());

		expect(created()).toMatchObject({
			name: 'acme',
			repoAccountId: 'repo-1',
			provisionerId: 'prov-1',
			harnessIds: ['h-a'],
			defaultFlow: 'mine',
			repo: { defaultBranch: 'main', agentsMdPath: 'AGENTS.md' },
			commands: {},
			workspace: { egressMode: 'allow', browserEnabled: false, services: [] }
		});
	});

	it('creates and hands the workshop to the page', async () => {
		const { onsaved } = setup();
		await fill();
		await userEvent.click(createBtn());

		await waitFor(() => expect(onsaved).toHaveBeenCalledOnce());
		expect(api.projects.update).not.toHaveBeenCalled();
		expect(toasts.list.at(-1)?.text).toBe('acme created');
	});

	it('needs what the browser requires', async () => {
		setup();
		await userEvent.click(createBtn());
		expect(api.projects.create).not.toHaveBeenCalled();
	});

	it('copes with nothing to pick from', async () => {
		setup({ integrations: [], flows: [] });
		expect(screen.getByText(/No flows exist yet/)).toBeInTheDocument();
		expect(screen.getAllByRole('link').length).toBeGreaterThanOrEqual(3);
		await fill();
		await userEvent.click(createBtn());
		expect(api.projects.create).not.toHaveBeenCalled();
	});
});

describe('ProjectForm, validation before the engine', () => {
	it('refuses duplicate command names, sending nothing and going to the section', async () => {
		edit({ commands: { test: 'a', lint: 'b' } });
		const keys = screen.getAllByRole('textbox', { name: 'Key' });
		await userEvent.clear(keys[1]);
		await userEvent.type(keys[1], 'test');
		await userEvent.click(saveBtn());

		expect(screen.getByText('Command names must be unique: test.')).toBeInTheDocument();
		expect(api.projects.update).not.toHaveBeenCalled();
		expect(location.hash).toBe('#workshop');
		expect(step(/Workshop/)).toHaveAttribute('aria-current', 'step');
	});

	it('refuses a service with something to fix, and goes strict', async () => {
		edit();
		await userEvent.click(screen.getByRole('button', { name: /Add a service/ }));
		expect(screen.queryByText(/Start is required/)).not.toBeInTheDocument();

		await userEvent.click(saveBtn());

		expect(
			screen.getByText('A service needs fixing before this can be saved.')
		).toBeInTheDocument();
		expect(screen.getByText(/Start is required/)).toBeInTheDocument();
		expect(api.projects.update).not.toHaveBeenCalled();
		expect(location.hash).toBe('#services');
	});

	it('goes to the services when the engine refuses one', async () => {
		vi.mocked(api.projects.update).mockRejectedValue(new Error('project: service web: bad'));
		edit();
		await retype('Workshop name', 'acme');
		await userEvent.click(saveBtn());

		await screen.findByText('Couldn’t save this setup');
		expect(location.hash).toBe('#services');
	});

	it('stays put on any other refusal', async () => {
		vi.mocked(api.projects.update).mockRejectedValue(new Error('name taken'));
		edit();
		await retype('Workshop name', 'acme');
		await userEvent.click(saveBtn());

		await screen.findByText('name taken');
		expect(location.hash).toBe('');
	});

	it("shows a failed defaults fetch as the form's error", async () => {
		vi.mocked(api.egress.defaults).mockRejectedValue(new Error('down'));
		edit();
		await userEvent.click(screen.getByRole('button', { name: /Add sensible defaults/ }));
		expect(await screen.findByText('Couldn’t save this setup')).toBeInTheDocument();
		expect(screen.getByText('down')).toBeInTheDocument();
	});
});

describe('ProjectForm, the step rail', () => {
	it('has six steps for a new workshop, a seventh once it exists', () => {
		const { unmount } = setup();
		const nav = screen.getByRole('navigation', { name: 'Setup steps' });
		expect(within(nav).getAllByRole('button')).toHaveLength(6);
		expect(document.querySelector('section#secrets')).toBeNull();
		unmount();

		edit();
		expect(step(/Secrets/)).toBeInTheDocument();
		expect(document.querySelector('section#secrets')).toBeInTheDocument();
	});

	it('jumps to a section and records it in the address', async () => {
		edit();
		await userEvent.click(step(/Execution/));
		expect(step(/Execution/)).toHaveAttribute('aria-current', 'step');
		expect(location.hash).toBe('#execution');
		expect(replaceState).toHaveBeenCalledWith('#execution', {});
	});

	it('opens on the section the address names', async () => {
		history.replaceState(null, '', '#browser');
		edit();
		await waitFor(() => expect(step(/Browser/)).toHaveAttribute('aria-current', 'step'));
	});

	it('ignores an address that names no section', async () => {
		history.replaceState(null, '', '#nope');
		edit();
		expect(step(/Repository/)).toHaveAttribute('aria-current', 'step');
	});

	it('sums the sections up as they are filled', async () => {
		edit({ workspace: { ...stored().workspace, egressMode: 'deny', browserEnabled: true } });
		expect(step(/Repository/)).toHaveTextContent('git.test/a');
		expect(step(/Workshop/)).toHaveTextContent('billing');
		expect(step(/Execution/)).toHaveTextContent('1 harness');
		expect(step(/Workspace/)).toHaveTextContent('denylist egress');
		expect(step(/Services/)).toHaveTextContent('none declared');
		expect(step(/Browser/)).toHaveTextContent('on');

		await userEvent.click(screen.getByRole('button', { name: /Add a service/ }));
		expect(step(/Services/)).toHaveTextContent('1 service');
	});

	it('ticks a section only once it is complete', async () => {
		setup();
		const tick = /✓/;
		expect(step(/Repository/)).not.toHaveTextContent(tick);

		await userEvent.type(screen.getByLabelText('Repo URL'), 'https://a.git');
		expect(step(/Repository/)).not.toHaveTextContent(tick);
		await userEvent.type(screen.getByLabelText('Locator'), '1');
		expect(step(/Repository/)).toHaveTextContent(tick);
	});
});
