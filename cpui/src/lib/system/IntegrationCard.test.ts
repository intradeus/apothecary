import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api, linkedTicketsOf } from '$lib/api';
import type { KindDescriptor } from '$lib/domain';
import { confirmState } from '$lib/ui/confirm.svelte';
import { toasts } from '$lib/ui/toast.svelte';
import { anIntegration, confirmed } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import IntegrationCard from './IntegrationCard.svelte';
import { readTests, writeTest } from './testResults';

vi.mock('$lib/api', () => ({
	api: {
		integrations: {
			test: vi.fn(),
			remove: vi.fn(),
			update: vi.fn(),
			statuses: vi.fn(),
			types: vi.fn()
		},
		credentials: { providers: vi.fn(), list: vi.fn() }
	},
	linkedTicketsOf: vi.fn(),
	isInUse: vi.fn()
}));

const integration = anIntegration({
	id: 'i1',
	name: 'Work',
	config: { base_url: 'https://x.test', credentials: ['c1', 'c9'] },
	secrets: { token: 'glpat-…abcd' }
});
const descriptor: KindDescriptor = {
	port: 'repository',
	kind: 'gitlab',
	label: 'GitLab',
	fields: [
		{ name: 'base_url', label: 'Base URL', secret: false, required: true },
		{ name: 'token', label: 'Token', secret: true, required: true },
		{
			name: 'credentials',
			label: 'Credentials',
			secret: false,
			required: false,
			type: 'credentials'
		}
	]
};
const setup = (over: Record<string, unknown> = {}) => {
	const onchanged = vi.fn();
	render(IntegrationCard, { integration, descriptor, onchanged, ...over });
	return { onchanged };
};
const test = () => screen.getByRole('button', { name: 'Test' });
const del = () => screen.getByRole('button', { name: 'Delete…' });

beforeEach(() => {
	vi.resetAllMocks();
	localStorage.clear();
	toasts.list = [];
	confirmState.current = null;
});

describe('IntegrationCard, the summary', () => {
	it('labels config and secrets from the descriptor, credentials by name', () => {
		setup({ credentialNames: { c1: 'Team key' } });
		expect(screen.getByText('Work')).toBeInTheDocument();
		expect(screen.getByText('GitLab')).toBeInTheDocument();
		expect(screen.getByText('Base URL')).toBeInTheDocument();
		expect(screen.getByText('https://x.test')).toBeInTheDocument();
		expect(screen.getByText('Token')).toBeInTheDocument();
		expect(screen.getByText('glpat-…abcd')).toBeInTheDocument();
		expect(screen.getByText('Team key, c9')).toBeInTheDocument();
	});

	it('falls back to raw keys and the kind for an unregistered kind', () => {
		setup({ descriptor: undefined });
		expect(screen.getByText('gitlab')).toBeInTheDocument();
		expect(screen.getByText('base_url')).toBeInTheDocument();
	});

	it('says who uses it, or that nobody does', () => {
		const { unmount } = render(IntegrationCard, {
			integration,
			usedBy: ['billing', 'web'],
			onchanged: vi.fn()
		});
		expect(screen.getByText('used by billing, web')).toBeInTheDocument();
		unmount();
		setup();
		expect(screen.getByText('not used by any workshop')).toBeInTheDocument();
	});

	it('says nothing about use for a ticket source', () => {
		setup({ integration: { ...integration, port: 'ticketsource' } });
		expect(screen.queryByText('not used by any workshop')).not.toBeInTheDocument();
	});
});

describe('IntegrationCard, testing', () => {
	it('starts untested', () => {
		setup();
		expect(screen.getByText('not tested yet')).toBeInTheDocument();
	});

	it('picks up the last result this browser saw', () => {
		writeTest('i1', { ok: true, at: Date.now() });
		setup();
		expect(screen.getByText('ok')).toBeInTheDocument();
	});

	it('shows a pass, saves it and confirms with a toast', async () => {
		vi.mocked(api.integrations.test).mockResolvedValue({ ok: true, warnings: [] } as never);
		setup();
		await userEvent.click(test());

		expect(await screen.findByText('ok')).toBeInTheDocument();
		expect(readTests().i1.ok).toBe(true);
		expect(toasts.list.at(-1)?.text).toBe('Work: test passed');
	});

	it('shows a pass it could not fully verify', async () => {
		vi.mocked(api.integrations.test).mockResolvedValue({
			ok: true,
			warnings: ['no probe']
		} as never);
		setup();
		await userEvent.click(test());

		expect(await screen.findByText('ok, unverified')).toBeInTheDocument();
		expect(toasts.list.at(-1)?.text).toBe('Work: passed, partly unverified');
	});

	it('shows the failure, whether reported or thrown', async () => {
		vi.mocked(api.integrations.test).mockResolvedValueOnce({
			ok: false,
			error: '401 bad token'
		} as never);
		setup();
		await userEvent.click(test());
		expect(await screen.findByText('401 bad token')).toBeInTheDocument();
		expect(toasts.list.at(-1)).toMatchObject({ text: 'Work: test failed', tone: 'bad' });

		vi.mocked(api.integrations.test).mockRejectedValueOnce(new Error('unreachable'));
		await userEvent.click(test());
		expect(await screen.findByText('unreachable')).toBeInTheDocument();
		expect(readTests().i1).toMatchObject({ ok: false, error: 'unreachable' });
	});

	it('holds the button while it tests', async () => {
		const run = deferred<never>();
		vi.mocked(api.integrations.test).mockReturnValue(run.promise);
		setup();
		await userEvent.click(test());

		expect(test()).toBeDisabled();
		expect(screen.getByText('testing…')).toBeInTheDocument();
		run.resolve({ ok: true } as never);
		await waitFor(() => expect(test()).toBeEnabled());
	});
});

describe('IntegrationCard, editing', () => {
	it('opens the form inline and closes it', async () => {
		setup();
		await userEvent.click(screen.getByRole('button', { name: 'Edit' }));
		expect(screen.getByRole('button', { name: 'Save changes' })).toBeInTheDocument();

		await userEvent.click(screen.getByRole('button', { name: 'Close' }));
		await waitFor(() =>
			expect(screen.queryByRole('button', { name: 'Save changes' })).not.toBeInTheDocument()
		);
	});

	it('tells the page after a save and closes', async () => {
		vi.mocked(api.integrations.update).mockResolvedValue(integration);
		vi.mocked(api.credentials.providers).mockResolvedValue([]);
		vi.mocked(api.credentials.list).mockResolvedValue([]);
		const { onchanged } = setup();
		await userEvent.click(screen.getByRole('button', { name: 'Edit' }));
		await userEvent.click(screen.getByRole('button', { name: 'Save changes' }));

		await waitFor(() => expect(onchanged).toHaveBeenCalledOnce());
		expect(await screen.findByRole('button', { name: 'Edit' })).toBeInTheDocument();
	});

	it('says an unregistered kind cannot be edited', async () => {
		setup({ descriptor: undefined });
		await userEvent.click(screen.getByRole('button', { name: 'Edit' }));
		expect(screen.queryByRole('button', { name: 'Save changes' })).not.toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Close' })).toHaveAttribute('aria-pressed', 'true');
	});
});

describe('IntegrationCard, deleting', () => {
	it('refuses up front while a workshop uses it', async () => {
		setup({ usedBy: ['billing'] });
		await userEvent.click(del());
		expect(
			screen.getByText(
				/In use by billing\. Pick another repository in that workshop’s setup first\./
			)
		).toBeInTheDocument();
		expect(confirmState.current).toBeNull();
	});

	it('words the refusal for several workshops', async () => {
		setup({ usedBy: ['billing', 'web'] });
		await userEvent.click(del());
		expect(screen.getByText(/those workshops’ setup/)).toBeInTheDocument();
	});

	it('does nothing when the dialog is declined', async () => {
		setup();
		await userEvent.click(del());
		await confirmed(false);
		await Promise.resolve();
		expect(api.integrations.remove).not.toHaveBeenCalled();
	});

	it('removes it, forgets its result and tells the page', async () => {
		writeTest('i1', { ok: true, at: 1 });
		vi.mocked(api.integrations.remove).mockResolvedValue(undefined);
		const { onchanged } = setup();
		await userEvent.click(del());
		const asked = await confirmed(true);

		expect(asked.title).toBe('Delete Work?');
		await waitFor(() => expect(onchanged).toHaveBeenCalledOnce());
		expect(api.integrations.remove).toHaveBeenCalledWith('i1');
		expect(readTests().i1).toBeUndefined();
		expect(toasts.list.at(-1)?.text).toBe('Work deleted');
	});

	it('warns differently for a ticket source', async () => {
		setup({ integration: { ...integration, port: 'ticketsource' } });
		await userEvent.click(del());
		const tickets = await confirmed(false);
		expect(tickets.body).toContain('Work');
		expect(tickets.body).not.toBe('');
	});

	it('shows a refusal on the card', async () => {
		vi.mocked(api.integrations.remove).mockRejectedValue(new Error('in use'));
		const { onchanged } = setup();
		await userEvent.click(del());
		await confirmed(true);

		expect(await screen.findByText('in use')).toBeInTheDocument();
		expect(onchanged).not.toHaveBeenCalled();
	});

	it('asks before unlinking the tickets that mirror a ticket source, then deletes', async () => {
		const source = { ...integration, port: 'ticketsource' as const, kind: 'jira' };
		const refusal = new Error('tickets mirror it');
		vi.mocked(api.integrations.remove).mockRejectedValueOnce(refusal).mockResolvedValue(undefined);
		vi.mocked(linkedTicketsOf).mockReturnValue({ tickets: 3, activeRuns: 1 });
		const { onchanged } = setup({ integration: source });
		await userEvent.click(del());
		await confirmed(true);
		const unlink = await confirmed(true);

		expect(unlink.title).toBe('Unlink 3 tickets?');
		expect(unlink.body).toContain('become Apothecary tickets');
		expect(unlink.body).toContain('1 run on them is still going');
		await waitFor(() => expect(onchanged).toHaveBeenCalledOnce());
		expect(linkedTicketsOf).toHaveBeenCalledWith(refusal);
		expect(api.integrations.remove).toHaveBeenLastCalledWith('i1', { unlinkTickets: true });
	});

	it('keeps a ticket source when the unlinking is declined', async () => {
		vi.mocked(api.integrations.remove).mockRejectedValue(new Error('tickets mirror it'));
		vi.mocked(linkedTicketsOf).mockReturnValue({ tickets: 1, activeRuns: 0 });
		const { onchanged } = setup({ integration: { ...integration, port: 'ticketsource' } });
		await userEvent.click(del());
		await confirmed(true);
		await confirmed(false);
		await Promise.resolve();

		expect(api.integrations.remove).toHaveBeenCalledOnce();
		expect(onchanged).not.toHaveBeenCalled();
	});
});

describe('IntegrationCard, a ticket tracker', () => {
	it('opens its statuses and types', async () => {
		vi.mocked(api.integrations.statuses).mockResolvedValue({ statuses: [], warnings: [] });
		vi.mocked(api.integrations.types).mockResolvedValue([]);
		setup({ integration: { ...integration, port: 'ticketsource', kind: 'jira' } });
		await userEvent.click(screen.getByRole('button', { name: 'Statuses & types' }));
		expect(await screen.findByRole('region', { name: 'Statuses' })).toBeInTheDocument();
		expect(api.integrations.statuses).toHaveBeenCalledWith('i1', false);
	});

	it('offers no test, edit or delete on the internal tracker', () => {
		setup({
			integration: {
				...integration,
				port: 'ticketsource',
				kind: 'internal',
				config: {},
				secrets: {}
			},
			descriptor: undefined
		});
		expect(screen.queryByRole('button', { name: 'Test' })).not.toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Edit' })).not.toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Delete…' })).not.toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Statuses & types' })).toBeInTheDocument();
		expect(screen.getByText('built in')).toBeInTheDocument();
	});
});
