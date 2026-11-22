import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { AssistantStatus, Skillset } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { choose } from '$test/fixtures/app.svelte';
import Page from './+page.svelte';

vi.mock('$lib/api', () => ({
	api: {
		assistant: { status: vi.fn(), setChoice: vi.fn() },
		skillsets: { list: vi.fn(), versions: vi.fn() }
	}
}));

const status = (over: Partial<AssistantStatus> = {}): AssistantStatus => ({
	enabled: true,
	choice: { integration: 'h1', model: '', skillsets: [] },
	integrations: [
		{ id: 'h1', name: 'mine', kind: 'claude-code' },
		{ id: 'h2', name: 'other', kind: 'opencode' }
	],
	models: ['sonnet-ish', 'opus-ish'],
	pod: { state: 'running' },
	repos: [],
	pages: {},
	...over
});

beforeEach(() => {
	vi.resetAllMocks();
	toasts.list = [];
	vi.mocked(api.assistant.status).mockResolvedValue(status());
	vi.mocked(api.assistant.setChoice).mockResolvedValue(undefined);
	vi.mocked(api.skillsets.list).mockResolvedValue([
		{ id: 's1', name: 'review', description: 'Code review', headSeq: 3 } as Skillset
	]);
	vi.mocked(api.skillsets.versions).mockResolvedValue([]);
});

describe('System → Assistant', () => {
	it('lists the person’s harnesses and models', async () => {
		render(Page);
		await userEvent.click(await screen.findByRole('combobox', { name: 'Harness' }));
		expect(screen.getByRole('option', { name: /other · opencode/ })).toBeInTheDocument();
		await userEvent.keyboard('{Escape}');
		await userEvent.click(screen.getByRole('combobox', { name: 'Model' }));
		expect(screen.getByRole('option', { name: /the harness’s default/ })).toBeInTheDocument();
		expect(screen.getByRole('option', { name: /opus-ish/ })).toBeInTheDocument();
		expect(screen.getByText(/Changes apply to new conversations/)).toBeInTheDocument();
	});

	it('saves a picked harness at once and shows its models', async () => {
		render(Page);
		await screen.findByRole('combobox', { name: 'Harness' });
		vi.mocked(api.assistant.status).mockResolvedValue(
			status({
				choice: { integration: 'h2', model: '', skillsets: [] },
				models: ['oc-model']
			})
		);
		await choose('Harness', /other · opencode/);
		expect(api.assistant.setChoice).toHaveBeenCalledWith({
			integration: 'h2',
			model: '',
			skillsets: []
		});
		await waitFor(() => expect(api.assistant.status).toHaveBeenCalledTimes(2));
		await userEvent.click(screen.getByRole('combobox', { name: 'Model' }));
		expect(await screen.findByRole('option', { name: /oc-model/ })).toBeInTheDocument();
		expect(screen.queryByRole('option', { name: /opus-ish/ })).not.toBeInTheDocument();
	});

	it('saves the picked skillsets with the harness and model', async () => {
		render(Page);
		await screen.findByRole('combobox', { name: 'Model' });
		await choose('Model', /opus-ish/);
		await userEvent.click(await screen.findByRole('checkbox', { name: /review/ }));
		await userEvent.click(screen.getByRole('button', { name: 'Save' }));
		expect(api.assistant.setChoice).toHaveBeenCalledWith({
			integration: 'h1',
			model: 'opus-ish',
			skillsets: [{ id: 's1' }]
		});
		await waitFor(() => expect(toasts.list.at(-1)?.text).toBe('Saved'));
	});

	it('keeps an unsaved skillset pick out of a harness change until Save', async () => {
		render(Page);
		await userEvent.click(await screen.findByRole('checkbox', { name: /review/ }));
		vi.mocked(api.assistant.status).mockResolvedValue(
			status({ choice: { integration: 'h2', model: '', skillsets: [] } })
		);
		await choose('Harness', /other · opencode/);
		expect(api.assistant.setChoice).toHaveBeenLastCalledWith({
			integration: 'h2',
			model: '',
			skillsets: []
		});
		await waitFor(() => expect(api.assistant.status).toHaveBeenCalledTimes(2));
		expect(screen.getByRole('checkbox', { name: /review/ })).toBeChecked();
		await userEvent.click(screen.getByRole('button', { name: 'Save' }));
		expect(api.assistant.setChoice).toHaveBeenLastCalledWith({
			integration: 'h2',
			model: '',
			skillsets: [{ id: 's1' }]
		});
	});

	it('shows a failed save', async () => {
		vi.mocked(api.assistant.setChoice).mockRejectedValue(new Error('nope'));
		render(Page);
		await userEvent.click(await screen.findByRole('button', { name: 'Save' }));
		expect(await screen.findByRole('alert')).toHaveTextContent('nope');
	});

	it('shows the saved choice on load', async () => {
		vi.mocked(api.assistant.status).mockResolvedValue(
			status({ choice: { integration: 'h2', model: 'opus-ish', skillsets: [{ id: 's1' }] } })
		);
		render(Page);
		expect(await screen.findByRole('combobox', { name: 'Harness' })).toHaveTextContent(
			'other · opencode'
		);
		expect(screen.getByRole('combobox', { name: 'Model' })).toHaveTextContent('opus-ish');
		expect(await screen.findByRole('checkbox', { name: /review/ })).toBeChecked();
	});

	it('explains why the assistant is off', async () => {
		vi.mocked(api.assistant.status).mockResolvedValue(
			status({ enabled: false, reason: 'not enabled on this instance', integrations: [] })
		);
		render(Page);
		expect(await screen.findByText(/not enabled on this instance/)).toBeInTheDocument();
		expect(screen.getByText('The assistant is off')).toBeInTheDocument();
	});

	it('does not repeat “choose a harness” while there is one to choose', async () => {
		vi.mocked(api.assistant.status).mockResolvedValue(
			status({
				enabled: false,
				reason: 'choose a harness',
				choice: { integration: '', model: '', skillsets: [] }
			})
		);
		render(Page);
		await screen.findByRole('combobox', { name: 'Harness' });
		expect(screen.queryByText(/choose a harness/)).not.toBeInTheDocument();
	});

	it('points to the integrations without a harness', async () => {
		vi.mocked(api.assistant.status).mockResolvedValue(status({ integrations: [] }));
		render(Page);
		expect(await screen.findByText(/Add a Claude Code or opencode/)).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'Integrations' })).toHaveAttribute(
			'href',
			'/system/integrations'
		);
		expect(screen.queryByRole('combobox')).not.toBeInTheDocument();
	});

	it('shows a failed load and retries', async () => {
		vi.mocked(api.assistant.status).mockRejectedValueOnce(new Error('down'));
		render(Page);
		expect(await screen.findByText('down')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /retry/i }));
		expect(await screen.findByRole('combobox', { name: 'Harness' })).toBeInTheDocument();
	});
});
