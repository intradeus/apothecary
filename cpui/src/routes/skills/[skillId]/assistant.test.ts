// The skill page beside the real assistant (page.test.ts stubs it): the
// workshop picked inside the page's mode is the one its conversation reads.
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from '$app/state';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { aProject } from '$test/fixtures';
import type { AssistantStatus } from '$lib/domain';
import AssistantDock from '$lib/assistant/AssistantDock.svelte';
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
	vi.mocked(api.assistant.open).mockImplementation(async (kind, _subject, workshop) => ({
		session:
			kind === 'general'
				? { ...session('g1', ''), kind: 'general' as const }
				: session(workshop ? 's2' : 's1', workshop ?? ''),
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

describe('the skill page in the assistant’s page mode', () => {
	it('reads the workshop picked inside it, without closing it', async () => {
		render(Page);
		render(AssistantDock);
		await userEvent.click(screen.getByRole('button', { name: 'Ask the assistant' }));
		const dock = screen.getByRole('complementary', { name: 'Assistant' });
		const skillCalls = () =>
			vi.mocked(api.assistant.open).mock.calls.filter((c) => c[0] === 'skill');
		await waitFor(() => expect(skillCalls()).toHaveLength(1));
		expect(skillCalls()[0][2]).toBeUndefined();

		await userEvent.click(
			within(dock).getByRole('combobox', { name: 'Read a workshop’s repositories' })
		);
		await userEvent.click(screen.getByRole('option', { name: 'shop' }));
		await waitFor(() => expect(skillCalls()).toHaveLength(2));
		expect(api.assistant.status).toHaveBeenLastCalledWith('p1');
		expect(skillCalls()[1][2]).toBe('p1');

		const box = await within(dock).findByRole('textbox', {
			name: 'What should this skill hold?'
		});
		await waitFor(() => expect(box).toBeEnabled());
		await userEvent.type(box, 'release notes{Enter}');
		expect(api.assistant.send).toHaveBeenCalledWith('s2', 'release notes', expect.anything());
		expect(screen.getByRole('complementary', { name: 'Assistant' })).toBeInTheDocument();
	});
});
