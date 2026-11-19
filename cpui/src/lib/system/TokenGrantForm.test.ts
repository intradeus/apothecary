import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import type { Permission, Token } from '$lib/domain';
import { aProject } from '$test/fixtures';
import { deferred } from '$test/helpers';
import TokenGrantForm from './TokenGrantForm.svelte';

vi.mock('$lib/api', () => ({
	api: { auth: { permissions: vi.fn(), createToken: vi.fn() } }
}));

const perm = (action: string, over: Partial<Permission> = {}): Permission => ({
	action,
	kind: action.split(':')[0],
	doc: `Doc of ${action}.`,
	grantable: true,
	purposes: [],
	...over
});
const PERMS = [
	perm('account:read'),
	perm('account:manage'),
	perm('run:read', { purposes: ['read', 'tickets', 'runs', 'authoring'] }),
	perm('run:operate', { purposes: ['runs'] }),
	perm('run:shell', { grantable: false }),
	perm('ticket:read', { purposes: ['read', 'tickets', 'runs', 'authoring'] }),
	perm('ticket:write', { purposes: ['tickets', 'runs'] }),
	perm('widget:spin')
];
const minted = { token: { id: 't1', name: 'ci', prefix: 'apo_x' } as Token, secret: 's3cret' };

const nameField = () => screen.getByRole('textbox', { name: 'Token name' });
const submit = () => screen.getByRole('button', { name: /Mint a token/ });
const limited = () => userEvent.click(screen.getByRole('radio', { name: 'Limited' }));
const mcp = () => userEvent.click(screen.getByRole('radio', { name: 'MCP client' }));
const box = (name: RegExp | string) => screen.getByRole('checkbox', { name });

function setup() {
	const oncreated = vi.fn();
	render(TokenGrantForm, { oncreated });
	return { oncreated };
}

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.auth.permissions).mockResolvedValue(PERMS);
	vi.mocked(api.auth.createToken).mockResolvedValue(minted);
	fleet.projects = [aProject({ id: 'p1', name: 'billing' }), aProject({ id: 'p2', name: 'web' })];
});

describe('TokenGrantForm', () => {
	it('mints a full-access token by default, without reading the permissions', async () => {
		const { oncreated } = setup();
		expect(screen.getByRole('radio', { name: 'Full access' })).toHaveAttribute(
			'aria-checked',
			'true'
		);
		expect(screen.getByText(/Everything your account may do/)).toBeInTheDocument();
		await userEvent.type(nameField(), '  ci  ');
		await userEvent.click(submit());

		await waitFor(() => expect(oncreated).toHaveBeenCalledWith({ ...minted, mcp: false }));
		expect(api.auth.createToken).toHaveBeenCalledWith('ci', { full: true });
		expect(api.auth.permissions).not.toHaveBeenCalled();
		expect(nameField()).toHaveValue('');
	});

	it('shows the permissions loading, then offers only what a token may hold, by kind', async () => {
		const read = deferred<Permission[]>();
		vi.mocked(api.auth.permissions).mockReturnValue(read.promise);
		setup();
		await limited();
		expect(screen.getByText(/Only the actions you tick/)).toBeInTheDocument();
		expect(screen.queryByRole('checkbox')).not.toBeInTheDocument();
		read.resolve(PERMS);

		const runs = await screen.findByRole('group', { name: 'Runs' });
		expect(runs).toHaveTextContent('run:read — Doc of run:read.');
		expect(screen.getByRole('group', { name: 'Account' })).toBeInTheDocument();
		expect(screen.getByRole('group', { name: 'Tickets' })).toBeInTheDocument();
		// A kind the catalogue has no word for is headed by its own.
		expect(screen.getByRole('group', { name: 'widget' })).toBeInTheDocument();
		expect(screen.queryByRole('checkbox', { name: /account:manage/ })).not.toBeInTheDocument();
		expect(screen.queryByRole('checkbox', { name: /run:shell/ })).not.toBeInTheDocument();
	});

	it('mints a read-only token for an MCP client: the actions the engine marks', async () => {
		const read = deferred<Permission[]>();
		vi.mocked(api.auth.permissions).mockReturnValue(read.promise);
		const { oncreated } = setup();
		await userEvent.type(nameField(), 'laptop agent');
		await mcp();
		expect(screen.getByText(/It never answers a gate/)).toBeInTheDocument();
		expect(submit()).toBeDisabled();
		read.resolve(PERMS);

		expect(await screen.findByText('run:read · ticket:read')).toBeInTheDocument();
		expect(screen.queryByRole('checkbox', { name: /run:operate/ })).not.toBeInTheDocument();
		await userEvent.click(box('billing'));
		await userEvent.click(submit());

		await waitFor(() => expect(oncreated).toHaveBeenCalledWith({ ...minted, mcp: true }));
		expect(api.auth.createToken).toHaveBeenCalledWith('laptop agent', {
			full: false,
			actions: ['run:read', 'ticket:read'],
			projectIds: ['p1']
		});
	});

	it('mints an MCP client token for a purpose: the reads and that purpose’s writes', async () => {
		const { oncreated } = setup();
		await userEvent.type(nameField(), 'runner');
		await mcp();
		await screen.findByText('run:read · ticket:read');
		await userEvent.click(screen.getByRole('radio', { name: 'Runs' }));
		expect(screen.getByText(/starts, pauses, resumes/)).toBeInTheDocument();
		expect(
			screen.getByText('run:read · run:operate · ticket:read · ticket:write')
		).toBeInTheDocument();
		await userEvent.click(screen.getByRole('radio', { name: 'Tickets' }));
		expect(screen.getByText('run:read · ticket:read · ticket:write')).toBeInTheDocument();
		await userEvent.click(submit());
		await waitFor(() => expect(oncreated).toHaveBeenCalledWith({ ...minted, mcp: true }));
		expect(api.auth.createToken).toHaveBeenCalledWith('runner', {
			full: false,
			actions: ['run:read', 'ticket:read', 'ticket:write'],
			projectIds: []
		});
		// A purpose whose writes the engine names none of holds the reads.
		await userEvent.type(nameField(), 'author');
		await mcp();
		await userEvent.click(screen.getByRole('radio', { name: 'Authoring' }));
		expect(screen.getByText('run:read · ticket:read')).toBeInTheDocument();
	});

	it('will not mint an MCP client token when the engine marks no action', async () => {
		vi.mocked(api.auth.permissions).mockResolvedValue([perm('run:read')]);
		setup();
		await userEvent.type(nameField(), 'agent');
		await mcp();
		await waitFor(() => expect(api.auth.permissions).toHaveBeenCalledOnce());
		expect(submit()).toBeDisabled();
	});

	it('shows a failed permissions read under MCP client too', async () => {
		vi.mocked(api.auth.permissions).mockRejectedValueOnce(new Error('engine down'));
		setup();
		await mcp();
		expect(await screen.findByText('engine down')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByText('run:read · ticket:read')).toBeInTheDocument();
	});

	it('will not mint a limited token with no action', async () => {
		setup();
		await userEvent.type(nameField(), 'ci');
		await limited();
		await screen.findByRole('group', { name: 'Runs' });
		expect(submit()).toBeDisabled();
		expect(screen.getByText('Tick at least one action.')).toBeInTheDocument();

		await userEvent.click(box(/run:read/));
		expect(submit()).toBeEnabled();
		expect(screen.queryByText('Tick at least one action.')).not.toBeInTheDocument();
		await userEvent.click(box(/run:read/));
		expect(submit()).toBeDisabled();
	});

	it('sends the ticked actions and every workshop when none is ticked', async () => {
		const { oncreated } = setup();
		await userEvent.type(nameField(), 'ci');
		await limited();
		await userEvent.click(await screen.findByRole('checkbox', { name: /ticket:read/ }));
		await userEvent.click(box(/run:read/));
		expect(screen.getByText(/None ticked: every workshop/)).toBeInTheDocument();
		await userEvent.click(submit());

		await waitFor(() => expect(oncreated).toHaveBeenCalled());
		expect(api.auth.createToken).toHaveBeenCalledWith('ci', {
			full: false,
			actions: ['ticket:read', 'run:read'],
			projectIds: []
		});
		// The form starts over at full access.
		expect(screen.getByRole('radio', { name: 'Full access' })).toHaveAttribute(
			'aria-checked',
			'true'
		);
	});

	it('sends the chosen workshops as the token’s workshops', async () => {
		setup();
		await userEvent.type(nameField(), 'ci');
		await limited();
		await userEvent.click(await screen.findByRole('checkbox', { name: /run:read/ }));
		const workshops = screen.getByRole('group', { name: 'Workshops' });
		expect(workshops).toHaveTextContent('billing');
		await userEvent.click(box('web'));
		await userEvent.click(submit());

		await waitFor(() =>
			expect(api.auth.createToken).toHaveBeenCalledWith('ci', {
				full: false,
				actions: ['run:read'],
				projectIds: ['p2']
			})
		);
	});

	it('offers no workshop choice when there is none to choose', async () => {
		fleet.projects = [];
		setup();
		await limited();
		await screen.findByRole('group', { name: 'Runs' });
		expect(screen.queryByRole('group', { name: 'Workshops' })).not.toBeInTheDocument();
	});

	it('reads the permissions once, however often Limited is chosen', async () => {
		setup();
		await limited();
		await screen.findByRole('group', { name: 'Runs' });
		await userEvent.click(screen.getByRole('radio', { name: 'Full access' }));
		expect(screen.queryByRole('group', { name: 'Runs' })).not.toBeInTheDocument();
		await limited();
		expect(screen.getByRole('group', { name: 'Runs' })).toBeInTheDocument();
		expect(api.auth.permissions).toHaveBeenCalledOnce();
	});

	it('shows a failed permissions read, and retries it', async () => {
		vi.mocked(api.auth.permissions).mockRejectedValueOnce(new Error('engine down'));
		setup();
		await limited();
		expect(
			await screen.findByText('Couldn’t load the actions a token can hold')
		).toBeInTheDocument();
		expect(screen.getByText('engine down')).toBeInTheDocument();
		expect(submit()).toBeDisabled();

		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByRole('group', { name: 'Runs' })).toBeInTheDocument();
		expect(screen.queryByText('engine down')).not.toBeInTheDocument();
	});

	it('shows a refused mint and keeps what was typed', async () => {
		vi.mocked(api.auth.createToken).mockRejectedValue(new Error('name taken'));
		const { oncreated } = setup();
		await userEvent.type(nameField(), 'ci');
		await userEvent.click(submit());

		expect(await screen.findByText('name taken')).toBeInTheDocument();
		expect(nameField()).toHaveValue('ci');
		expect(oncreated).not.toHaveBeenCalled();
		expect(submit()).toBeEnabled();
	});

	it('is busy while the mint is in flight', async () => {
		const mint = deferred<typeof minted>();
		vi.mocked(api.auth.createToken).mockReturnValue(mint.promise);
		setup();
		await userEvent.type(nameField(), 'ci');
		await userEvent.click(submit());
		expect(submit()).toBeDisabled();
		expect(submit()).toHaveAttribute('aria-busy', 'true');
		mint.resolve(minted);
		await waitFor(() => expect(submit()).toBeEnabled());
	});
});
