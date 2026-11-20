import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { goto } from '$app/navigation';
import { page } from '$app/state';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { aProject } from '$test/fixtures';
import type { Skill, SkillDetail } from '$lib/domain';
import { encodeText } from '$lib/skills/files';
import Page from './+page.svelte';

vi.mock('$app/state', async () => {
	const { bindable } = await import('$test/fixtures/app.svelte');
	return { page: bindable({ params: { skillId: 'new' }, url: new URL('http://localhost/') }) };
});
vi.mock('$lib/api', () => ({
	api: {
		skills: { get: vi.fn(), create: vi.fn(), update: vi.fn() },
		skillsets: { list: vi.fn() },
		assistant: { setSubject: vi.fn() }
	}
}));
vi.mock('$lib/skills/SkillFileEditor.svelte', () => import('$test/fixtures/YamlEditorStub.svelte'));
vi.mock(
	'$lib/assistant/AssistantPanel.svelte',
	() => import('$test/fixtures/AssistantPanelStub.svelte')
);

const md = (name: string) => `---\nname: ${name}\ndescription: Notes.\n---\n\nDo it.\n`;
const aSkill = (over: Partial<Skill> = {}): Skill => ({
	id: 'sk1',
	name: 'notes',
	description: 'Notes.',
	files: [],
	hash: 'h',
	createdAt: 'T',
	updatedAt: 'T',
	...over
});

const openIt = () => userEvent.click(screen.getByRole('button', { name: 'Assistant' }));
const panel = () => screen.getByRole('region', { name: 'Assistant' });
async function offer(fill: unknown) {
	const box = screen.getByRole('textbox', { name: 'fill' });
	await userEvent.clear(box);
	box.focus();
	await userEvent.paste(JSON.stringify(fill));
	await userEvent.click(screen.getByRole('button', { name: 'offer the fill' }));
}
const readPage = async () => {
	await userEvent.click(screen.getByRole('button', { name: 'read the page' }));
	return JSON.parse(screen.getByRole('status', { name: 'page state' }).textContent!);
};

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.skillsets.list).mockResolvedValue([]);
	vi.mocked(api.assistant.setSubject).mockResolvedValue({} as never);
	fleet.projects = [];
});

describe('the skill page’s assistant', () => {
	it('drafts a new skill: fills its name and files, and moves onto it once created', async () => {
		page.params.skillId = 'new';
		vi.mocked(api.skills.create).mockResolvedValue(aSkill({ id: 'sk9', name: 'release-notes' }));
		render(Page);
		await openIt();
		expect(panel().dataset).toMatchObject({ kind: 'skill', workshop: '' });
		expect(panel().dataset.subject).toMatch(/^draft-/);
		expect((await readPage()).files).toHaveLength(1);

		await offer({
			name: 'release-notes',
			files: [
				{ path: 'SKILL.md', content: md('release-notes') },
				{ path: 'notes.md', content: 'more' }
			]
		});
		expect(screen.getByLabelText('Name')).toHaveValue('release-notes');
		expect(screen.getByRole('button', { name: 'notes.md' })).toBeInTheDocument();
		expect(screen.getByRole('status', { name: 'changed' })).toHaveTextContent(
			'name,SKILL.md,notes.md'
		);

		await userEvent.click(screen.getByRole('button', { name: 'Create skill' }));
		await waitFor(() => expect(goto).toHaveBeenCalled());
		expect(api.skills.create).toHaveBeenCalledWith(
			expect.objectContaining({ name: 'release-notes' })
		);
		expect(api.assistant.setSubject).toHaveBeenCalledWith('s1', 'sk9');
	});

	it('is about the skill when it exists, reads it, and undoes a fill', async () => {
		page.params.skillId = 'sk1';
		const detail: SkillDetail = {
			skill: aSkill(),
			contents: [
				{ path: 'SKILL.md', bytes: encodeText(md('notes')) },
				{ path: 'logo.png', bytes: new Uint8Array([0, 1]) }
			]
		} as SkillDetail;
		vi.mocked(api.skills.get).mockResolvedValue(detail);
		render(Page);
		await screen.findByRole('button', { name: 'Assistant' });
		await openIt();
		expect(panel().dataset.subject).toBe('sk1');
		expect(await readPage()).toEqual({
			name: 'notes',
			files: [{ path: 'SKILL.md', content: md('notes') }],
			other_files: ['logo.png'],
			open_file: 'SKILL.md'
		});

		await offer({ name: 'notes', files: [{ path: 'SKILL.md', content: md('notes') + 'More.\n' }] });
		expect(screen.getByRole('textbox', { name: /SKILL\.md/ })).toHaveValue(md('notes') + 'More.\n');
		expect(screen.getByRole('button', { name: 'logo.png' })).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Save skill' })).toBeEnabled();

		await userEvent.click(screen.getByRole('button', { name: 'undo the fill' }));
		expect(screen.getByRole('textbox', { name: /SKILL\.md/ })).toHaveValue(md('notes'));

		// Filled and saved: nothing left to undo.
		await offer({
			name: 'notes',
			files: [{ path: 'SKILL.md', content: md('notes') + 'Saved.\n' }]
		});
		vi.mocked(api.skills.update).mockResolvedValue(aSkill());
		await userEvent.click(screen.getByRole('button', { name: 'Save skill' }));
		await waitFor(() => expect(api.skills.update).toHaveBeenCalled());
		await waitFor(() =>
			expect(screen.getByRole('status', { name: 'changed' })).toHaveTextContent('')
		);
	});

	it('still opens the skill when the conversation cannot move', async () => {
		page.params.skillId = 'new';
		vi.mocked(api.skills.create).mockResolvedValue(aSkill({ id: 'sk9', name: 'my-skill' }));
		vi.mocked(api.assistant.setSubject).mockRejectedValue(new Error('gone'));
		render(Page);
		await openIt();
		await userEvent.type(screen.getByLabelText('Name'), 'my-skill');
		await userEvent.click(screen.getByRole('button', { name: 'Create skill' }));
		await waitFor(() => expect(goto).toHaveBeenCalled());
	});

	it('reads an attached workshop’s repositories: a session of its own', async () => {
		page.params.skillId = 'new';
		fleet.projects = [aProject({ id: 'p1', name: 'shop' })];
		render(Page);
		await openIt();
		expect(panel().dataset.workshop).toBe('');
		await userEvent.click(screen.getByRole('combobox', { name: 'Read a workshop’s repositories' }));
		await userEvent.click(screen.getByRole('option', { name: 'shop' }));
		expect(panel().dataset.workshop).toBe('p1');
	});
});
