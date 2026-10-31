import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { ProjectSecret } from '$lib/domain';
import { confirmState } from '$lib/ui/confirm.svelte';
import { toasts } from '$lib/ui/toast.svelte';
import { confirmed } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import SecretsEditor from './SecretsEditor.svelte';

vi.mock('$lib/api', () => ({
	api: { projects: { secrets: vi.fn(), setSecret: vi.fn(), deleteSecret: vi.fn() } }
}));

const AT = '2026-10-31T12:00:00Z';
const secret = (name: string): ProjectSecret => ({ name, updatedAt: AT });
const nameBox = () => screen.getByRole('textbox', { name: 'Secret name' });
const valueBox = () => screen.getByPlaceholderText('value');
const add = () => screen.getByRole('button', { name: /Set secret/ });

async function setup(secrets = [secret('api_key')]) {
	vi.mocked(api.projects.secrets).mockResolvedValue(secrets);
	render(SecretsEditor, { projectId: 'p1' });
	await screen.findByRole('textbox', { name: 'Secret name' });
}

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.projects.setSecret).mockImplementation(async (_, name) => secret(name));
	toasts.list = [];
	confirmState.current = null;
});

describe('SecretsEditor, listing', () => {
	it('lists names, never values', async () => {
		await setup([secret('api_key')]);
		expect(api.projects.secrets).toHaveBeenCalledWith('p1');
		expect(screen.getByText('api_key')).toBeInTheDocument();
	});

	it('says when there are none', async () => {
		await setup([]);
		expect(screen.getByText('No secrets yet.')).toBeInTheDocument();
	});

	it('offers a retry when listing fails', async () => {
		vi.mocked(api.projects.secrets).mockRejectedValueOnce(new Error('down'));
		vi.mocked(api.projects.secrets).mockResolvedValueOnce([]);
		render(SecretsEditor, { projectId: 'p1' });

		expect(await screen.findByText('Couldn’t list the secrets')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByText('No secrets yet.')).toBeInTheDocument();
	});
});

describe('SecretsEditor, adding', () => {
	it('waits for a name and a value', async () => {
		await setup([]);
		expect(add()).toBeDisabled();
		await userEvent.type(nameBox(), 'token');
		expect(add()).toBeDisabled();
		await userEvent.type(valueBox(), 'x');
		expect(add()).toBeEnabled();
	});

	it('sets it, sorted in, and clears the row', async () => {
		await setup([secret('b'), secret('z')]);
		await userEvent.type(nameBox(), ' c ');
		await userEvent.type(valueBox(), ' v ');
		await userEvent.click(add());

		expect(api.projects.setSecret).toHaveBeenCalledWith('p1', 'c', ' v ');
		await waitFor(() => expect(nameBox()).toHaveValue(''));
		expect(valueBox()).toHaveValue('');
		expect(toasts.list.at(-1)?.text).toBe('c set');
		const names = screen.getAllByText(/^[bcz]$/).map((e) => e.textContent);
		expect(names).toEqual(['b', 'c', 'z']);
	});

	it('sets on Enter, not submitting the setup form around it', async () => {
		vi.mocked(api.projects.secrets).mockResolvedValue([]);
		const form = document.createElement('form');
		form.innerHTML = '<button type="submit">Save setup</button>';
		const submit = vi.fn((e: Event) => e.preventDefault());
		form.addEventListener('submit', submit);
		document.body.append(form);
		render(SecretsEditor, { props: { projectId: 'p1' }, target: form });

		await userEvent.type(await screen.findByRole('textbox', { name: 'Secret name' }), 'token');
		await userEvent.type(valueBox(), 'x{Enter}');
		form.remove();

		expect(api.projects.setSecret).toHaveBeenCalledOnce();
		expect(submit).not.toHaveBeenCalled();
	});

	it.each(['Bad', '1st', 'a b'])('refuses the name %j', async (name) => {
		await setup([]);
		await userEvent.type(nameBox(), name);
		await userEvent.type(valueBox(), 'x{Enter}');

		expect(add()).toBeDisabled();
		expect(api.projects.setSecret).not.toHaveBeenCalled();
		expect(nameBox()).toHaveClass('invalid');
	});

	it('refuses a name already taken, pointing to Replace', async () => {
		await setup([secret('token')]);
		await userEvent.type(nameBox(), 'token');
		await userEvent.type(valueBox(), 'x{Enter}');

		expect(
			screen.getByText('token exists — use Replace on its row to change its value.')
		).toBeInTheDocument();
		expect(api.projects.setSecret).not.toHaveBeenCalled();
	});

	it('shows a failure, keeps the row, and dismisses', async () => {
		vi.mocked(api.projects.setSecret).mockRejectedValue(new Error('vault down'));
		await setup([]);
		await userEvent.type(nameBox(), 'token');
		await userEvent.type(valueBox(), 'x');
		await userEvent.click(add());

		expect(await screen.findByText('The secret wasn’t saved')).toBeInTheDocument();
		expect(nameBox()).toHaveValue('token');
		await userEvent.click(screen.getByRole('button', { name: /dismiss/i }));
		expect(screen.queryByText('The secret wasn’t saved')).not.toBeInTheDocument();
	});

	it('holds the buttons while one is saving', async () => {
		const save = deferred<ProjectSecret>();
		vi.mocked(api.projects.setSecret).mockReturnValue(save.promise);
		await setup([secret('other')]);
		await userEvent.type(nameBox(), 'token');
		await userEvent.type(valueBox(), 'x');
		await userEvent.click(add());

		expect(add()).toBeDisabled();
		expect(screen.getByRole('button', { name: 'Replace value' })).toBeDisabled();
		expect(screen.getByRole('button', { name: 'Delete other' })).toBeDisabled();
		save.resolve(secret('token'));
		await waitFor(() =>
			expect(screen.getAllByRole('button', { name: 'Replace value' })[0]).toBeEnabled()
		);
	});
});

describe('SecretsEditor, replacing', () => {
	const open = () => userEvent.click(screen.getByRole('button', { name: 'Replace value' }));
	beforeEach(() => setup([secret('token')]));
	const field = () => screen.getByLabelText('New value for token');

	it('asks for the new value and saves it', async () => {
		await open();
		expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
		await userEvent.type(field(), 'new');
		await userEvent.click(screen.getByRole('button', { name: 'Save' }));

		expect(api.projects.setSecret).toHaveBeenCalledWith('p1', 'token', 'new');
		await waitFor(() => expect(screen.queryByLabelText('New value for token')).toBeNull());
		expect(toasts.list.at(-1)?.text).toBe('token replaced');
	});

	it('saves on Enter', async () => {
		await open();
		await userEvent.type(field(), 'new{Enter}');
		expect(api.projects.setSecret).toHaveBeenCalledOnce();
	});

	it('cancels without saving, and starts empty next time', async () => {
		await open();
		await userEvent.type(field(), 'typo');
		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		expect(api.projects.setSecret).not.toHaveBeenCalled();

		await open();
		expect(field()).toHaveValue('');
	});

	it('hides what is typed', async () => {
		await open();
		expect(field()).toHaveAttribute('type', 'password');
	});
});

describe('SecretsEditor, deleting', () => {
	const del = () => screen.getByRole('button', { name: 'Delete token' });

	it('does nothing when declined', async () => {
		await setup([secret('token')]);
		await userEvent.click(del());
		await confirmed(false);
		await Promise.resolve();
		expect(api.projects.deleteSecret).not.toHaveBeenCalled();
		expect(screen.getByText('token')).toBeInTheDocument();
	});

	it('removes it from the list', async () => {
		vi.mocked(api.projects.deleteSecret).mockResolvedValue(undefined as never);
		await setup([secret('token')]);
		await userEvent.click(del());
		const asked = await confirmed(true);

		expect(asked).toMatchObject({ title: 'Delete token?', tone: 'danger' });
		await waitFor(() => expect(screen.queryByText('token')).not.toBeInTheDocument());
		expect(api.projects.deleteSecret).toHaveBeenCalledWith('p1', 'token');
		expect(toasts.list.at(-1)).toMatchObject({ text: 'token deleted', tone: 'info' });
	});

	it('shows a failure and keeps the row', async () => {
		vi.mocked(api.projects.deleteSecret).mockRejectedValue(new Error('in use'));
		await setup([secret('token')]);
		await userEvent.click(del());
		await confirmed(true);

		expect(await screen.findByText('The secret wasn’t saved')).toBeInTheDocument();
		expect(screen.getByText('token')).toBeInTheDocument();
	});
});
