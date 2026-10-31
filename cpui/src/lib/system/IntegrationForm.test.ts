import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Credential, KindDescriptor, KindField, ProviderInfo } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { anIntegration } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import IntegrationForm from './IntegrationForm.svelte';

vi.mock('$lib/api', () => ({
	api: {
		integrations: { create: vi.fn(), update: vi.fn() },
		credentials: { providers: vi.fn(), list: vi.fn() }
	}
}));

const field = (over: Partial<KindField> & { name: string }): KindField => ({
	label: over.name,
	secret: false,
	required: false,
	...over
});
const descriptor = (fields: KindField[], over: Partial<KindDescriptor> = {}): KindDescriptor => ({
	port: 'repository',
	kind: 'gitlab',
	label: 'GitLab',
	fields,
	...over
});
const base = field({ name: 'base_url', label: 'Base URL', required: true });
const token = field({ name: 'token', label: 'Token', secret: true, required: true });
const creds = field({
	name: 'credentials',
	label: 'Credentials',
	type: 'credentials',
	required: true
});
const cred = (over: Partial<Credential> = {}) =>
	({
		id: 'c1',
		provider: 'anth',
		kind: 'api_key',
		name: 'Team key',
		status: 'ok',
		...over
	}) as Credential;

const add = () => screen.getByRole('button', { name: 'Add GitLab' });
const saveChanges = () => screen.getByRole('button', { name: 'Save changes' });
const createdBody = () => vi.mocked(api.integrations.create).mock.calls.at(-1)![0];
const updatedBody = () => vi.mocked(api.integrations.update).mock.calls.at(-1)![1];

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.integrations.create).mockImplementation(async (d) =>
		anIntegration({ name: d.name })
	);
	vi.mocked(api.integrations.update).mockImplementation(async (_, d) =>
		anIntegration({ name: d.name })
	);
	vi.mocked(api.credentials.providers).mockResolvedValue([{ id: 'anth' } as ProviderInfo]);
	vi.mocked(api.credentials.list).mockResolvedValue([cred()]);
	toasts.list = [];
});

describe('IntegrationForm, rendering from the descriptor', () => {
	it('draws an input per field, with the kind as the default name', () => {
		render(IntegrationForm, { descriptor: descriptor([base, token]), onsaved: vi.fn() });
		expect(screen.getByLabelText('Name')).toHaveValue('GitLab');
		expect(screen.getByLabelText(/Base URL/)).toBeRequired();
		expect(screen.getByLabelText(/Token/)).toHaveAttribute('type', 'password');
	});

	it('uses a field default, and does not require a field that has one', () => {
		const url = field({ name: 'url', label: 'URL', default: 'https://x.test', required: true });
		render(IntegrationForm, { descriptor: descriptor([url]), onsaved: vi.fn() });
		expect(screen.getByLabelText(/URL/)).toHaveValue('https://x.test');
		expect(screen.getByLabelText(/URL/)).not.toBeRequired();
	});

	it('uses a textarea for a multiline secret and any *_json secret', () => {
		render(IntegrationForm, {
			descriptor: descriptor([
				field({ name: 'key', label: 'Key', secret: true, multiline: true }),
				field({ name: 'sa_json', label: 'Service account', secret: true })
			]),
			onsaved: vi.fn()
		});
		expect(screen.getByLabelText('Key').tagName).toBe('TEXTAREA');
		expect(screen.getByLabelText('Service account').tagName).toBe('TEXTAREA');
	});

	it('turns URLs in help into links, leaving trailing punctuation out', () => {
		render(IntegrationForm, {
			descriptor: descriptor([
				field({ name: 'a', label: 'A', help: 'Make one at https://x.test/tokens, then paste it.' })
			]),
			onsaved: vi.fn()
		});
		expect(screen.getByRole('link', { name: 'https://x.test/tokens' })).toHaveAttribute(
			'href',
			'https://x.test/tokens'
		);
		expect(screen.getByText(/then paste it\./)).toBeInTheDocument();
	});

	it('opens the setup help for a new integration and folds it on an edit', () => {
		const d = descriptor([], { help: 'Create a token.' });
		const { container, unmount } = render(IntegrationForm, { descriptor: d, onsaved: vi.fn() });
		expect(container.querySelector('details')).toHaveAttribute('open');
		unmount();

		const edit = render(IntegrationForm, {
			descriptor: d,
			existing: anIntegration(),
			onsaved: vi.fn()
		});
		expect(edit.container.querySelector('details')).not.toHaveAttribute('open');
	});
});

describe('IntegrationForm, adding', () => {
	it('sends what was typed, trimmed, a secret as typed', async () => {
		const onsaved = vi.fn();
		render(IntegrationForm, { descriptor: descriptor([base, token]), onsaved });
		await userEvent.clear(screen.getByLabelText('Name'));
		await userEvent.type(screen.getByLabelText('Name'), ' Work ');
		await userEvent.type(screen.getByLabelText(/Base URL/), ' https://x.test ');
		await userEvent.type(screen.getByLabelText(/Token/), ' s3cret ');
		await userEvent.click(add());

		expect(createdBody()).toEqual({
			port: 'repository',
			kind: 'gitlab',
			name: 'Work',
			config: { base_url: 'https://x.test' },
			secrets: { token: ' s3cret ' }
		});
		expect(onsaved).toHaveBeenCalledOnce();
		expect(toasts.list.at(-1)?.text).toBe('Work added');
	});

	it('leaves empty optional fields out', async () => {
		render(IntegrationForm, {
			descriptor: descriptor([
				field({ name: 'opt', label: 'Optional' }),
				field({ name: 'sec', label: 'Secret', secret: true })
			]),
			onsaved: vi.fn()
		});
		await userEvent.click(add());
		expect(createdBody()).toMatchObject({ config: {}, secrets: {} });
	});

	it('needs its required fields', async () => {
		render(IntegrationForm, { descriptor: descriptor([base]), onsaved: vi.fn() });
		await userEvent.click(add());
		expect(api.integrations.create).not.toHaveBeenCalled();
	});

	it('holds the button and says Saving while it saves', async () => {
		const save = deferred<ReturnType<typeof anIntegration>>();
		vi.mocked(api.integrations.create).mockReturnValue(save.promise);
		render(IntegrationForm, { descriptor: descriptor([]), onsaved: vi.fn() });
		await userEvent.click(add());

		expect(screen.getByRole('button', { name: 'Saving…' })).toBeDisabled();
		save.resolve(anIntegration());
		await waitFor(() => expect(add()).toBeEnabled());
	});

	it('shows a refusal and stays', async () => {
		vi.mocked(api.integrations.create).mockRejectedValue(new Error('bad token'));
		const onsaved = vi.fn();
		render(IntegrationForm, { descriptor: descriptor([]), onsaved });
		await userEvent.click(add());

		expect(await screen.findByText('bad token')).toBeInTheDocument();
		expect(onsaved).not.toHaveBeenCalled();
		expect(add()).toBeEnabled();
	});

	it('shows Cancel only when the page wants one', async () => {
		const oncancel = vi.fn();
		render(IntegrationForm, { descriptor: descriptor([]), onsaved: vi.fn(), oncancel });
		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		expect(oncancel).toHaveBeenCalledOnce();
	});
});

describe('IntegrationForm, editing', () => {
	const existing = anIntegration({
		id: 'i1',
		name: 'Work',
		config: { base_url: 'https://old.test' },
		secrets: { token: 'glpat-…abcd' }
	});
	const setup = () =>
		render(IntegrationForm, { descriptor: descriptor([base, token]), existing, onsaved: vi.fn() });

	it('starts from what is stored, and shows a set secret as its mask', () => {
		setup();
		expect(screen.getByLabelText('Name')).toHaveValue('Work');
		expect(screen.getByLabelText(/Base URL/)).toHaveValue('https://old.test');
		expect(screen.getByText('•••• set')).toBeInTheDocument();
		expect(screen.getByText('glpat-…abcd')).toBeInTheDocument();
		expect(screen.queryByLabelText(/Token/)).toBeNull();
	});

	it('saves over it, sending no secret untouched', async () => {
		setup();
		await userEvent.click(saveChanges());

		expect(api.integrations.update).toHaveBeenCalledWith('i1', {
			name: 'Work',
			config: { base_url: 'https://old.test' },
			secrets: {}
		});
		expect(toasts.list.at(-1)?.text).toBe('Work saved');
	});

	it('sends a replaced secret', async () => {
		setup();
		await userEvent.click(screen.getByRole('button', { name: 'Replace' }));
		await userEvent.type(screen.getByLabelText(/Token/), 'new-one');
		await userEvent.click(saveChanges());
		expect(updatedBody().secrets).toEqual({ token: 'new-one' });
	});

	it('does not require a replacement once a secret is stored', async () => {
		setup();
		await userEvent.click(screen.getByRole('button', { name: 'Replace' }));
		expect(screen.getByLabelText(/Token/)).not.toBeRequired();
		await userEvent.click(saveChanges());
		expect(api.integrations.update).toHaveBeenCalledOnce();
	});

	it('goes back to the stored value on keep current, dropping what was typed', async () => {
		setup();
		await userEvent.click(screen.getByRole('button', { name: 'Replace' }));
		await userEvent.type(screen.getByLabelText(/Token/), 'typo');
		await userEvent.click(screen.getByRole('button', { name: 'keep current' }));

		expect(screen.getByText('•••• set')).toBeInTheDocument();
		await userEvent.click(saveChanges());
		expect(updatedBody().secrets).toEqual({});
	});
});

describe('IntegrationForm, a credentials field', () => {
	const harness = descriptor([creds], { port: 'harness', kind: 'claude', label: 'Claude' });
	const addClaude = () => screen.getByRole('button', { name: 'Add Claude' });

	it('offers only the credentials this kind takes', async () => {
		vi.mocked(api.credentials.list).mockResolvedValue([
			cred({ name: 'Team key' }),
			cred({ id: 'c2', provider: 'other', name: 'Not for this' })
		]);
		render(IntegrationForm, { descriptor: harness, onsaved: vi.fn() });

		expect(await screen.findByRole('checkbox', { name: /Team key/ })).toBeInTheDocument();
		expect(api.credentials.providers).toHaveBeenCalledWith('claude');
		expect(screen.queryByRole('checkbox', { name: /Not for this/ })).not.toBeInTheDocument();
	});

	it('asks for no credentials on a kind without such a field', () => {
		render(IntegrationForm, { descriptor: descriptor([base]), onsaved: vi.fn() });
		expect(api.credentials.list).not.toHaveBeenCalled();
	});

	it('marks a login, and one that needs signing in again', async () => {
		vi.mocked(api.credentials.list).mockResolvedValue([
			cred({ kind: 'oauth', status: 'disabled', name: 'Login' })
		]);
		render(IntegrationForm, { descriptor: harness, onsaved: vi.fn() });
		const row = await screen.findByRole('checkbox', { name: /Login/ });
		expect(row.closest('label')).toHaveTextContent('login');
		expect(row.closest('label')).toHaveTextContent('sign in again');
	});

	it('points to the providers page when none fits', async () => {
		vi.mocked(api.credentials.list).mockResolvedValue([]);
		render(IntegrationForm, { descriptor: harness, onsaved: vi.fn() });
		expect(
			await screen.findByRole('link', { name: /Add one under Model providers/ })
		).toHaveAttribute('href', '/system/providers');
	});

	it('refuses to save a required pick left empty', async () => {
		render(IntegrationForm, { descriptor: harness, onsaved: vi.fn() });
		await screen.findByRole('checkbox', { name: /Team key/ });
		await userEvent.click(addClaude());

		expect(screen.getByText('Pick at least one in Credentials.')).toBeInTheDocument();
		expect(api.integrations.create).not.toHaveBeenCalled();
	});

	it('sends the picks as config', async () => {
		vi.mocked(api.credentials.list).mockResolvedValue([cred(), cred({ id: 'c2', name: 'Other' })]);
		render(IntegrationForm, { descriptor: harness, onsaved: vi.fn() });
		await userEvent.click(await screen.findByRole('checkbox', { name: /Other/ }));
		await userEvent.click(screen.getByRole('checkbox', { name: /Team key/ }));
		await userEvent.click(screen.getByRole('checkbox', { name: /Other/ }));
		await userEvent.click(screen.getByRole('checkbox', { name: /Other/ }));
		await userEvent.click(addClaude());

		expect(createdBody().config).toEqual({ credentials: ['c1', 'c2'] });
	});

	it('starts from the stored picks', async () => {
		render(IntegrationForm, {
			descriptor: harness,
			existing: anIntegration({ config: { credentials: ['c1'] } }),
			onsaved: vi.fn()
		});
		expect(await screen.findByRole('checkbox', { name: /Team key/ })).toBeChecked();
	});

	it('shows a failure to read the credentials', async () => {
		vi.mocked(api.credentials.list).mockRejectedValue(new Error('down'));
		render(IntegrationForm, { descriptor: harness, onsaved: vi.fn() });
		expect(await screen.findByText('down')).toBeInTheDocument();
	});
});
