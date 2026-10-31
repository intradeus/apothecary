import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { ExtraRepo, Repo } from '$lib/domain';
import { anIntegration, bindable, choose } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import RepositorySection from './RepositorySection.svelte';

vi.mock('$lib/api', () => ({ api: { integrations: { repos: vi.fn() } } }));

const gitlab = anIntegration({ id: 'g', name: 'Gitlab', kind: 'gitlab' });
const github = anIntegration({ id: 'h', name: 'Github', kind: 'github' });
const repo = (over: Partial<Repo> = {}): Repo =>
	({
		name: 'acme/Billing',
		url: 'https://git.test/acme/billing.git',
		locator: '42',
		defaultBranch: 'trunk',
		...over
	}) as Repo;

const setup = (over: Record<string, unknown> = {}) => {
	const props = bindable({
		repoAccounts: [gitlab, github],
		repoAccountId: 'g',
		repoURL: '',
		locator: '',
		defaultBranch: 'main',
		agentsMD: 'AGENTS.md',
		name: '',
		extraRepos: [] as ExtraRepo[],
		...over
	});
	render(RepositorySection, props);
	return props;
};
const browse = () => screen.getByRole('button', { name: 'Browse repositories' });
async function open(repos = [repo()]) {
	vi.mocked(api.integrations.repos).mockResolvedValue(repos);
	await userEvent.click(browse());
}

beforeEach(() => {
	vi.resetAllMocks();
});

describe('RepositorySection, the fields', () => {
	it('binds each one', async () => {
		const props = setup();
		await choose('Repository account', 'Github (github)');
		await userEvent.type(screen.getByLabelText('Repo URL'), 'https://x.git');
		await userEvent.type(screen.getByLabelText('Locator'), '9');
		await userEvent.clear(screen.getByLabelText('Default branch'));
		await userEvent.type(screen.getByLabelText('Default branch'), 'dev');
		await userEvent.clear(screen.getByLabelText('AGENTS.md path'));
		await userEvent.type(screen.getByLabelText('AGENTS.md path'), 'docs/AGENTS.md');

		expect(props).toMatchObject({
			repoAccountId: 'h',
			repoURL: 'https://x.git',
			locator: '9',
			defaultBranch: 'dev',
			agentsMD: 'docs/AGENTS.md'
		});
	});

	it('requires the URL and the locator', () => {
		setup();
		expect(screen.getByLabelText('Repo URL')).toBeRequired();
		expect(screen.getByLabelText('Locator')).toBeRequired();
	});

	it('links to the integrations with no account, and has nothing to browse', () => {
		setup({ repoAccounts: [], repoAccountId: '' });
		expect(screen.getAllByRole('link')[0]).toHaveAttribute('href', '/system/integrations');
		expect(browse()).toBeDisabled();
	});
});

describe('RepositorySection, the browser', () => {
	it('lists what the account can see', async () => {
		setup();
		await open();
		expect(api.integrations.repos).toHaveBeenCalledWith('g');
		expect(await screen.findByText('acme/Billing')).toBeInTheDocument();
		expect(screen.getByText('trunk')).toBeInTheDocument();
	});

	it('holds the button while it loads', async () => {
		const list = deferred<Repo[]>();
		vi.mocked(api.integrations.repos).mockReturnValue(list.promise);
		setup();
		await userEvent.click(browse());

		expect(screen.getByRole('button', { name: 'Loading…' })).toBeDisabled();
		list.resolve([]);
		await waitFor(() => expect(browse()).toBeEnabled());
	});

	it('says when the token sees nothing', async () => {
		setup();
		await open([]);
		expect(await screen.findByText('No repositories visible to this token.')).toBeInTheDocument();
	});

	it('filters by name or URL, ignoring case', async () => {
		setup();
		await open([
			repo(),
			repo({ name: 'acme/web', url: 'https://git.test/other.git', locator: '43' })
		]);
		const filter = await screen.findByPlaceholderText('Filter 2 repositories');

		await userEvent.type(filter, 'BILLING');
		expect(screen.getByText('acme/Billing')).toBeInTheDocument();
		expect(screen.queryByText('acme/web')).not.toBeInTheDocument();

		await userEvent.clear(filter);
		await userEvent.type(filter, 'other.git');
		expect(screen.getByText('acme/web')).toBeInTheDocument();

		await userEvent.clear(filter);
		await userEvent.type(filter, 'zzz');
		expect(screen.getByText('No repository matches.')).toBeInTheDocument();
	});

	it('fills the fields from a pick, naming an unnamed workshop', async () => {
		const props = setup();
		await open();
		await userEvent.click(await screen.findByRole('button', { name: 'Use' }));

		expect(props).toMatchObject({
			repoURL: 'https://git.test/acme/billing.git',
			locator: '42',
			defaultBranch: 'trunk',
			name: 'Billing'
		});
		expect(screen.queryByText('acme/Billing')).not.toBeInTheDocument();
	});

	it('keeps a name already given', async () => {
		const props = setup({ name: 'Mine' });
		await open();
		await userEvent.click(await screen.findByRole('button', { name: 'Use' }));
		expect(props.name).toBe('Mine');
	});

	it('keeps the branch when the repository names none', async () => {
		const props = setup();
		await open([repo({ defaultBranch: '' })]);
		await userEvent.click(await screen.findByRole('button', { name: 'Use' }));
		expect(props.defaultBranch).toBe('main');
	});

	it('adds a pick as an extra repository, leaving the browser open', async () => {
		const props = setup();
		await open();
		await userEvent.click(await screen.findByRole('button', { name: /extra/ }));

		expect(props.extraRepos).toEqual([
			{
				name: 'billing',
				url: 'https://git.test/acme/billing.git',
				locator: '42',
				defaultBranch: 'trunk'
			}
		]);
		expect(screen.getByText('acme/Billing')).toBeInTheDocument();
	});

	it('closes', async () => {
		setup();
		await open();
		await userEvent.click(await screen.findByRole('button', { name: 'Close' }));
		expect(screen.queryByText('acme/Billing')).not.toBeInTheDocument();
	});

	it('offers a retry when listing fails', async () => {
		vi.mocked(api.integrations.repos).mockRejectedValueOnce(new Error('bad token'));
		setup();
		await userEvent.click(browse());
		expect(await screen.findByText('Couldn’t list repositories')).toBeInTheDocument();

		vi.mocked(api.integrations.repos).mockResolvedValue([repo()]);
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByText('acme/Billing')).toBeInTheDocument();
		expect(screen.queryByText('Couldn’t list repositories')).not.toBeInTheDocument();
	});
});

describe('RepositorySection, extra repositories', () => {
	it('adds a blank row on main', async () => {
		const props = setup();
		await userEvent.click(screen.getByRole('button', { name: /Add repository/ }));
		expect(props.extraRepos).toEqual([{ name: '', url: '', locator: '', defaultBranch: 'main' }]);
	});
});
