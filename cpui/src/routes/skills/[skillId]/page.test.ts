import { render, screen, waitFor, within } from '@testing-library/svelte';
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
import AssistantStub from '$test/fixtures/AssistantStub.svelte';

// The assistant's page mode, as the shell would hold it beside the page.
beforeEach(() => void render(AssistantStub));

vi.mock('$app/state', async () => {
	const { bindable } = await import('$test/fixtures/app.svelte');
	return { page: bindable({ params: { skillId: 'new' }, url: new URL('http://localhost/') }) };
});
vi.mock('$lib/api', () => ({
	api: {
		skills: { get: vi.fn(), create: vi.fn(), update: vi.fn() },
		skillsets: { list: vi.fn() }
	}
}));
vi.mock('$lib/skills/SkillFileEditor.svelte', () => import('$test/fixtures/YamlEditorStub.svelte'));

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

const openIt = () =>
	userEvent.click(screen.getByRole('button', { name: 'Fill with the assistant' }));
const panel = () => screen.getByRole('region', { name: 'Fill assistant' });
const filled = () => screen.queryByText(/^Filled:/);
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
	fleet.projects = [];
});

describe('the skill page’s assistant', () => {
	it('drafts a new skill: reads the unsaved editor, fills its name and files unsaved', async () => {
		page.params.skillId = 'new';
		vi.mocked(api.skills.create).mockResolvedValue(aSkill({ id: 'sk9', name: 'release-notes' }));
		render(Page);
		await userEvent.type(screen.getByLabelText('Name'), 'draft');
		await openIt();
		expect(panel().dataset).toMatchObject({ kind: 'skill', workshop: '' });
		expect(panel().dataset.subject).toMatch(/^draft-/);
		const read = await readPage();
		expect(read.name).toBe('draft');
		expect(read.files).toHaveLength(1);

		await offer({
			name: 'release-notes',
			files: [
				{ path: 'SKILL.md', content: md('release-notes') },
				{ path: 'notes.md', content: 'more' }
			]
		});
		expect(screen.getByLabelText('Name')).toHaveValue('release-notes');
		expect(screen.getByRole('button', { name: 'notes.md' })).toBeInTheDocument();
		expect(filled()).toHaveTextContent('name, SKILL.md, notes.md');
		expect(api.skills.create).not.toHaveBeenCalled();

		await userEvent.click(screen.getByRole('button', { name: 'Create skill' }));
		await waitFor(() => expect(goto).toHaveBeenCalled());
		expect(api.skills.create).toHaveBeenCalledWith(
			expect.objectContaining({ name: 'release-notes' })
		);
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
		await screen.findByRole('button', { name: 'Fill with the assistant' });
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
		expect(api.skills.update).not.toHaveBeenCalled();

		await userEvent.click(screen.getByRole('button', { name: 'Undo' }));
		expect(screen.getByRole('textbox', { name: /SKILL\.md/ })).toHaveValue(md('notes'));
		expect(filled()).not.toBeInTheDocument();

		// Filled and saved: nothing left to undo.
		await offer({
			name: 'notes',
			files: [{ path: 'SKILL.md', content: md('notes') + 'Saved.\n' }]
		});
		vi.mocked(api.skills.update).mockResolvedValue(aSkill());
		await userEvent.click(screen.getByRole('button', { name: 'Save skill' }));
		await waitFor(() => expect(api.skills.update).toHaveBeenCalled());
		await waitFor(() => expect(filled()).not.toBeInTheDocument());
	});

	it('picks, inside the modal, a workshop whose repositories it reads', async () => {
		page.params.skillId = 'new';
		fleet.projects = [aProject({ id: 'p1', name: 'shop' })];
		render(Page);
		const picker = 'Read a workshop’s repositories';
		expect(screen.queryByRole('combobox', { name: picker })).not.toBeInTheDocument();
		await openIt();
		expect(panel().dataset.workshop).toBe('');
		await userEvent.click(within(panel()).getByRole('combobox', { name: picker }));
		await userEvent.click(screen.getByRole('option', { name: 'shop' }));
		expect(panel().dataset.workshop).toBe('p1');
	});

	it('offers no workshop picker without workshops', async () => {
		page.params.skillId = 'new';
		render(Page);
		await openIt();
		expect(
			screen.queryByRole('combobox', { name: 'Read a workshop’s repositories' })
		).not.toBeInTheDocument();
	});
});
