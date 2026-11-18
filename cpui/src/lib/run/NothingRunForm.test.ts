import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { aFlow, aWorkshop, bindable } from '$test/fixtures/app.svelte';
import { aViewerProject } from '$test/fixtures';
import NothingRunForm from './NothingRunForm.svelte';

vi.mock('$lib/api', () => ({
	api: {
		flows: { list: vi.fn(), get: vi.fn(), binding: vi.fn() },
		projects: { get: vi.fn(), branches: vi.fn(), mergeRequests: vi.fn() },
		integrations: { list: vi.fn(), models: vi.fn() },
		skillsets: { list: vi.fn(), versions: vi.fn() }
	}
}));

const sandbox = aFlow({ name: 'workspace-only', builtin: true, operatesOn: 'nothing' });
const builder = aFlow({ name: 'builder', operatesOn: 'ticket' });

function setup(over: Record<string, unknown> = {}) {
	const props = bindable({
		projectId: 'p1',
		flow: '',
		title: '',
		branch: '',
		onBranch: false,
		newBranch: '',
		blocked: false,
		params: {},
		skillsets: [],
		namePlaceholder: 'manual run - 2026-11-17 18:40',
		tail: 'gxjj',
		...over
	});
	render(NothingRunForm, { props });
	return props;
}
const branchPick = () => screen.findByRole('combobox', { name: 'Start from' });
const itself = (branch = 'main') =>
	screen.getByRole('checkbox', { name: 'Checkout branch ' + branch });
const newBranch = () => screen.getByRole('textbox', { name: 'New branch' });

beforeEach(() => {
	vi.resetAllMocks();
	const shop = aWorkshop({ id: 'p1', defaultFlow: 'builder' });
	shop.repo.defaultBranch = 'main';
	fleet.projects = [shop];
	vi.mocked(api.flows.list).mockResolvedValue([sandbox, builder]);
	vi.mocked(api.flows.get).mockResolvedValue(sandbox);
	vi.mocked(api.flows.binding).mockResolvedValue({
		projectId: 'p1',
		flowName: 'workspace-only',
		params: {},
		updatedAt: ''
	});
	vi.mocked(api.projects.get).mockResolvedValue(shop);
	vi.mocked(api.projects.branches).mockResolvedValue([
		{ name: 'dev', isDefault: false },
		{ name: 'main', isDefault: true }
	]);
	vi.mocked(api.integrations.list).mockResolvedValue([]);
	vi.mocked(api.skillsets.list).mockResolvedValue([]);
});

describe('NothingRunForm', () => {
	it('offers only the flows whose runs start on nothing, picking one that fits', async () => {
		const props = setup();
		// The workshop's default (builder) starts on a ticket: it is not offered.
		await waitFor(() => expect(props.flow).toBe('workspace-only'));
		await userEvent.click(screen.getByRole('combobox', { name: 'Flow' }));
		const offered = screen.getAllByRole('option');
		expect(offered).toHaveLength(1);
		expect(offered[0]).toHaveTextContent('workspace-only');
	});

	it('asks for no name, showing what the run will be called', async () => {
		const props = setup();
		const name = screen.getByRole('textbox', { name: 'Name' });
		expect(name).toHaveAttribute('placeholder', 'manual run - 2026-11-17 18:40');
		await userEvent.type(name, 'Poke at it');
		expect(props.title).toBe('Poke at it');
	});

	it('starts from the repository’s default branch, on a fresh branch off it', async () => {
		const props = setup();
		expect(await branchPick()).toHaveTextContent('main');
		expect(props.branch).toBe('main');
		expect(itself()).not.toBeChecked();
		expect(screen.getByText(/fresh branch of its own, forked from main/)).toBeInTheDocument();
	});

	it('takes another branch, and working on it itself', async () => {
		const props = setup();
		await userEvent.click(await branchPick());
		await userEvent.click(screen.getByRole('option', { name: 'dev' }));
		await userEvent.click(itself('dev'));

		expect(props.branch).toBe('dev');
		expect(props.onBranch).toBe(true);
		expect(screen.getByText(/Nothing is forked: the workspace is on dev/)).toBeInTheDocument();
	});

	it('writes the fresh branch’s name for the person, after the run’s name', async () => {
		const props = setup();
		await branchPick();
		expect(newBranch()).toHaveValue('apothecary/manual-run-2026-11-17-18-40-gxjj');
		expect(props.newBranch).toBe('apothecary/manual-run-2026-11-17-18-40-gxjj');
		expect(props.blocked).toBe(false);

		// It follows the name while no one wrote another.
		await userEvent.type(screen.getByRole('textbox', { name: 'Name' }), 'Poke at it');
		expect(newBranch()).toHaveValue('apothecary/poke-at-it-gxjj');
		expect(props.newBranch).toBe('apothecary/poke-at-it-gxjj');
	});

	it('takes the person’s own name for it, and stops following the run’s', async () => {
		const props = setup();
		await branchPick();
		await userEvent.clear(newBranch());
		// Emptied, the engine names it.
		expect(props.newBranch).toBe('');
		expect(newBranch()).toHaveAttribute('placeholder', 'named after the run');
		await userEvent.type(newBranch(), 'spike/cache');
		await userEvent.type(screen.getByRole('textbox', { name: 'Name' }), 'Poke at it');

		expect(props.newBranch).toBe('spike/cache');
		expect(newBranch()).toHaveValue('spike/cache');
		expect(newBranch()).not.toBeInvalid();
	});

	it('puts the written name back when the checkbox is ticked or unticked', async () => {
		const props = setup();
		await branchPick();
		await userEvent.clear(newBranch());
		await userEvent.type(newBranch(), 'spike/cache');

		await userEvent.click(itself());
		// Checked out itself, there is no fresh branch to name.
		expect(screen.queryByRole('textbox', { name: 'New branch' })).not.toBeInTheDocument();
		expect(props.newBranch).toBe('apothecary/manual-run-2026-11-17-18-40-gxjj');
		await userEvent.click(itself());
		expect(newBranch()).toHaveValue('apothecary/manual-run-2026-11-17-18-40-gxjj');

		// Written again, it is theirs again.
		await userEvent.type(newBranch(), '-2');
		expect(props.newBranch).toBe('apothecary/manual-run-2026-11-17-18-40-gxjj-2');
	});

	it('keeps the person’s own name when the form is shown again', async () => {
		const props = setup({ newBranch: 'spike/cache' });
		await branchPick();
		expect(newBranch()).toHaveValue('spike/cache');
		expect(props.newBranch).toBe('spike/cache');
	});

	it('refuses the name of a branch the repository holds: that is the checkbox’s to say', async () => {
		const props = setup();
		await branchPick();
		await userEvent.clear(newBranch());
		await userEvent.type(newBranch(), ' dev ');
		expect(await screen.findByText(/^dev already exists\. To work on it/)).toBeInTheDocument();
		expect(newBranch()).toBeInvalid();
		await waitFor(() => expect(props.blocked).toBe(true));

		// Another name is free again.
		await userEvent.type(newBranch(), 'x');
		await waitFor(() => expect(props.blocked).toBe(false));
		expect(screen.queryByText(/already exists/)).not.toBeInTheDocument();

		// And so is the branch checked out itself.
		await userEvent.clear(newBranch());
		await userEvent.type(newBranch(), 'main');
		await waitFor(() => expect(props.blocked).toBe(true));
		await userEvent.click(itself());
		await waitFor(() => expect(props.blocked).toBe(false));
	});

	it('knows the default branch is held even when the repository will not list them', async () => {
		vi.mocked(api.projects.branches).mockRejectedValue(new Error('no token'));
		const props = setup();
		await screen.findByText(/The branches could not be listed\./);
		await userEvent.clear(newBranch());
		await userEvent.type(newBranch(), 'main');
		expect(await screen.findByText(/^main already exists/)).toBeInTheDocument();
		await waitFor(() => expect(props.blocked).toBe(true));
	});

	it('names the checkbox for the branch picked, or the default it cannot name', async () => {
		setup();
		await branchPick();
		expect(itself('main')).not.toBeChecked();
		await userEvent.click(await branchPick());
		await userEvent.click(screen.getByRole('option', { name: 'dev' }));
		expect(itself('dev')).toBeInTheDocument();
	});

	it('holds the start where the role may not start a run (run:operate)', async () => {
		fleet.projects = [aViewerProject({ id: 'p1' })];
		const props = setup();
		expect(screen.getByText('Your role in this workshop does not allow this')).toBeInTheDocument();
		await waitFor(() => expect(props.blocked).toBe(true));
	});

	it('says the default branch when the workshop is not known yet', async () => {
		fleet.projects = [];
		vi.mocked(api.projects.branches).mockRejectedValue(new Error('no token'));
		setup();
		expect(
			await screen.findByRole('checkbox', { name: 'Checkout the default branch' })
		).toBeInTheDocument();
	});

	it('leaves the branch to the engine when the repository will not list them', async () => {
		vi.mocked(api.projects.branches).mockRejectedValue(new Error('no token'));
		const props = setup();
		expect(await screen.findByText(/The branches could not be listed\./)).toBeInTheDocument();
		expect(props.branch).toBe('');
		expect(screen.getByRole('textbox', { name: 'Start from' })).toBeDisabled();
		expect(screen.getByRole('textbox', { name: 'Start from' })).toHaveValue('main');
		// The default branch still names what the run forks from.
		expect(screen.getByText(/forked from main/)).toBeInTheDocument();
	});
});
