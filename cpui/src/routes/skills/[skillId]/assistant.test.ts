// The skill page with its real fill modal (page.test.ts stubs it): the
// workshop picked inside the open modal is the one the conversation reads.
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from '$app/state';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { aProject } from '$test/fixtures';
import type { AssistantStatus } from '$lib/domain';
import Page from './+page.svelte';

vi.mock('$app/state', async () => {
	const { bindable } = await import('$test/fixtures/app.svelte');
	return { page: bindable({ params: { skillId: 'new' }, url: new URL('http://localhost/') }) };
});
vi.mock('$lib/api', () => ({
	api: {
		skills: { get: vi.fn(), create: vi.fn(), update: vi.fn() },
		skillsets: { list: vi.fn() },
		assistant: {
			status: vi.fn(),
			open: vi.fn(),
			warm: vi.fn(),
			send: vi.fn(),
			cancel: vi.fn(),
			stream: vi.fn()
		}
	}
}));
vi.mock('$lib/skills/SkillFileEditor.svelte', () => import('$test/fixtures/YamlEditorStub.svelte'));

const status = (): AssistantStatus => ({
	enabled: true,
	choice: { integration: 'int-1', model: '', skillsets: [] },
	integrations: [{ id: 'int-1', name: 'mine', kind: 'claude-code' }],
	models: [],
	pod: { state: 'running' },
	repos: [],
	pages: { skill: { presets: [], repos: true } }
});
const session = (id: string, workshop: string) => ({
	id,
	kind: 'skill' as const,
	workshop,
	subject: 'draft-1',
	createdAt: 'T',
	updatedAt: 'T'
});

beforeEach(() => {
	vi.resetAllMocks();
	page.params.skillId = 'new';
	fleet.projects = [aProject({ id: 'p1', name: 'shop' })];
	vi.mocked(api.skillsets.list).mockResolvedValue([]);
	vi.mocked(api.assistant.status).mockResolvedValue(status());
	vi.mocked(api.assistant.open).mockImplementation(async (_kind, _subject, workshop) => ({
		session: session(workshop ? 's2' : 's1', workshop ?? ''),
		messages: [],
		busy: false
	}));
	vi.mocked(api.assistant.warm).mockResolvedValue(undefined as never);
	vi.mocked(api.assistant.send).mockImplementation(async (id, text) => ({
		sessionId: id,
		seq: 1,
		role: 'person',
		text,
		costUsd: 0,
		at: 'T'
	}));
	vi.mocked(api.assistant.stream).mockImplementation((_fn, ready) => {
		ready?.();
		return () => {};
	});
});

describe('the skill page’s fill modal', () => {
	it('reads the workshop picked inside it, without closing it', async () => {
		render(Page);
		await userEvent.click(screen.getByRole('button', { name: 'Fill with the assistant' }));
		const modal = screen.getByRole('dialog');
		await waitFor(() => expect(api.assistant.open).toHaveBeenCalledTimes(1));
		expect(api.assistant.status).toHaveBeenLastCalledWith(undefined);
		expect(vi.mocked(api.assistant.open).mock.calls[0][2]).toBeUndefined();

		await userEvent.click(
			within(modal).getByRole('combobox', { name: 'Read a workshop’s repositories' })
		);
		await userEvent.click(screen.getByRole('option', { name: 'shop' }));
		await waitFor(() => expect(api.assistant.open).toHaveBeenCalledTimes(2));
		expect(api.assistant.status).toHaveBeenLastCalledWith('p1');
		expect(api.assistant.open).toHaveBeenLastCalledWith('skill', expect.any(String), 'p1', {
			fresh: true
		});

		const box = await within(modal).findByRole('textbox', {
			name: 'What should this page hold?'
		});
		await userEvent.type(box, 'release notes{Enter}');
		expect(api.assistant.send).toHaveBeenCalledWith('s2', 'release notes', expect.anything());
	});
});
