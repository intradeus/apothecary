import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { goto } from '$app/navigation';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { session } from '$lib/app/session.svelte';
import type { Me, Member, Project } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { aProject, aViewerProject, PROJECT_ACTIONS } from '$test/fixtures';
import { choose, confirmed } from '$test/fixtures/app.svelte';
import MembersTab from './MembersTab.svelte';

vi.mock('$lib/api', () => ({
	api: {
		projects: {
			members: vi.fn(),
			addMember: vi.fn(),
			updateMember: vi.fn(),
			removeMember: vi.fn(),
			leave: vi.fn()
		}
	}
}));

const aMember = (over: Partial<Member> = {}): Member => ({
	userId: 'u2',
	handle: 'bo',
	name: 'Bo Brew',
	role: 'viewer',
	createdAt: '2026-10-31T12:00:00Z',
	...over
});
const owned = aProject({ id: 'p1', name: 'Apothecary' });
/** A workshop shared with the caller as a maintainer (project:members, not owner). */
const maintained: Project = aViewerProject({
	id: 'p1',
	name: 'Apothecary',
	access: {
		role: 'maintainer',
		actions: [...PROJECT_ACTIONS.filter((a) => a !== 'project:delete'), 'project:leave']
	}
});
const viewed = aViewerProject({ id: 'p1', name: 'Apothecary' });

const toastTexts = () => toasts.list.map((x) => x.text);
const rows = () => within(screen.getByRole('list', { name: 'Members' })).getAllByRole('listitem');

beforeEach(() => {
	vi.mocked(api.projects.members).mockResolvedValue([
		aMember(),
		aMember({ userId: 'u3', handle: 'cy', name: 'Cy', role: 'maintainer' })
	]);
	session.me = { user: { id: 'u1', handle: 'lu', name: 'Lu Owner', role: 'member' } } as Me;
	toasts.list = [];
});

describe('MembersTab', () => {
	it('lists the owner first, then every member with their role', async () => {
		render(MembersTab, { project: owned });
		await waitFor(() => expect(rows()).toHaveLength(3));
		const [o, bo, cy] = rows();
		expect(o).toHaveTextContent('@lu');
		expect(o).toHaveTextContent('Lu Owner');
		expect(o).toHaveTextContent('Owner');
		expect(bo).toHaveTextContent('@bo');
		expect(bo).toHaveTextContent('Bo Brew');
		expect(within(bo).getByRole('combobox', { name: 'Role of bo' })).toHaveTextContent('Viewer');
		expect(within(cy).getByRole('combobox', { name: 'Role of cy' })).toHaveTextContent(
			'Maintainer'
		);
		expect(api.projects.members).toHaveBeenCalledWith('p1');
	});

	it('names the sharing owner when the workshop is someone else’s', async () => {
		render(MembersTab, { project: viewed });
		await waitFor(() => expect(rows()[0]).toHaveTextContent('@ada'));
		expect(rows()[0]).not.toHaveTextContent('Lu Owner');
	});

	it('says nobody else is in an unshared workshop', async () => {
		vi.mocked(api.projects.members).mockResolvedValue([]);
		render(MembersTab, { project: owned });
		expect(await screen.findByText('Nobody else yet')).toBeInTheDocument();
	});

	it('shows a load failure and retries', async () => {
		vi.mocked(api.projects.members).mockRejectedValueOnce(new Error('down'));
		render(MembersTab, { project: owned });
		expect(await screen.findByText('down')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /retry/i }));
		await waitFor(() => expect(rows()).toHaveLength(3));
	});

	it('adds a member by handle or email with the chosen role', async () => {
		vi.mocked(api.projects.addMember).mockResolvedValue(
			aMember({ userId: 'u4', handle: 'di', name: 'Di', role: 'operator' })
		);
		render(MembersTab, { project: owned });
		await waitFor(() => expect(rows()).toHaveLength(3));
		const add = screen.getByRole('button', { name: /Add member/ });
		expect(add).toBeDisabled();
		await userEvent.type(screen.getByLabelText('Handle or email'), ' di@example.com ');
		await choose('Role', 'Operator');
		await userEvent.click(add);
		expect(api.projects.addMember).toHaveBeenCalledWith('p1', 'di@example.com', 'operator');
		await waitFor(() => expect(rows()).toHaveLength(4));
		expect(toastTexts()).toContain('di added');
		expect(screen.getByLabelText('Handle or email')).toHaveValue('');
	});

	it('shows the engine’s refusal beside the form', async () => {
		vi.mocked(api.projects.addMember).mockRejectedValue(new Error('no such account'));
		render(MembersTab, { project: owned });
		await userEvent.type(screen.getByLabelText('Handle or email'), 'nobody');
		await userEvent.click(screen.getByRole('button', { name: /Add member/ }));
		expect(await screen.findByText('no such account')).toBeInTheDocument();
	});

	it('changes a role after confirmation', async () => {
		vi.mocked(api.projects.updateMember).mockResolvedValue(aMember({ role: 'operator' }));
		render(MembersTab, { project: owned });
		await waitFor(() => expect(rows()).toHaveLength(3));
		await choose('Role of bo', 'Operator');
		expect((await confirmed(true)).title).toBe('Make bo a Operator?');
		await waitFor(() =>
			expect(api.projects.updateMember).toHaveBeenCalledWith('p1', 'u2', 'operator')
		);
		await waitFor(() => expect(toastTexts()).toContain('bo is now a Operator'));
	});

	it('keeps the role when the change is cancelled or refused', async () => {
		render(MembersTab, { project: owned });
		await waitFor(() => expect(rows()).toHaveLength(3));
		await choose('Role of bo', 'Operator');
		await confirmed(false);
		await waitFor(() =>
			expect(screen.getByRole('combobox', { name: 'Role of bo' })).toHaveTextContent('Viewer')
		);
		expect(api.projects.updateMember).not.toHaveBeenCalled();

		vi.mocked(api.projects.updateMember).mockRejectedValue(new Error('this needs project:members'));
		await choose('Role of bo', 'Operator');
		await confirmed(true);
		await waitFor(() => expect(toastTexts()).toContain('this needs project:members'));
		expect(screen.getByRole('combobox', { name: 'Role of bo' })).toHaveTextContent('Viewer');
	});

	it('removes a member after confirmation', async () => {
		vi.mocked(api.projects.removeMember).mockResolvedValue(undefined);
		render(MembersTab, { project: owned });
		await waitFor(() => expect(rows()).toHaveLength(3));
		await userEvent.click(screen.getByRole('button', { name: 'Remove bo' }));
		expect((await confirmed(true)).title).toBe('Remove bo?');
		await waitFor(() => expect(rows()).toHaveLength(2));
		expect(api.projects.removeMember).toHaveBeenCalledWith('p1', 'u2');
		expect(toastTexts()).toContain('bo removed');
	});

	it('removes nothing when cancelled, and toasts a refusal', async () => {
		render(MembersTab, { project: owned });
		await waitFor(() => expect(rows()).toHaveLength(3));
		await userEvent.click(screen.getByRole('button', { name: 'Remove bo' }));
		await confirmed(false);
		expect(api.projects.removeMember).not.toHaveBeenCalled();

		vi.mocked(api.projects.removeMember).mockRejectedValue(new Error('gone'));
		await userEvent.click(screen.getByRole('button', { name: 'Remove bo' }));
		await confirmed(true);
		await waitFor(() => expect(toastTexts()).toContain('gone'));
		expect(rows()).toHaveLength(3);
	});

	it('hides every control without project:members', async () => {
		render(MembersTab, { project: viewed });
		await waitFor(() => expect(rows()).toHaveLength(3));
		expect(screen.queryByLabelText('Handle or email')).toBeNull();
		expect(screen.queryByRole('button', { name: /Add member/ })).toBeNull();
		expect(screen.queryByRole('combobox')).toBeNull();
		expect(screen.queryByRole('button', { name: /^Remove/ })).toBeNull();
		expect(rows()[1]).toHaveTextContent('Viewer');
	});

	it('offers the maintainer role to the owner', async () => {
		render(MembersTab, { project: owned });
		await userEvent.click(screen.getByRole('combobox', { name: 'Role' }));
		expect(await screen.findByRole('option', { name: 'Maintainer' })).toBeInTheDocument();
	});

	it('keeps maintainers out of a non-owner’s reach', async () => {
		render(MembersTab, { project: maintained });
		await waitFor(() => expect(rows()).toHaveLength(3));
		await userEvent.click(screen.getByRole('combobox', { name: 'Role' }));
		expect(await screen.findByRole('option', { name: 'Operator' })).toBeInTheDocument();
		expect(screen.queryByRole('option', { name: 'Maintainer' })).toBeNull();
		await userEvent.keyboard('{Escape}');
		const cy = rows()[2];
		expect(within(cy).queryByRole('combobox')).toBeNull();
		expect(within(cy).queryByRole('button', { name: 'Remove cy' })).toBeNull();
		expect(cy).toHaveTextContent('Maintainer');
		expect(screen.getByRole('button', { name: 'Remove bo' })).toBeInTheDocument();
	});

	it('lets a member leave, then goes back to the workshops', async () => {
		vi.mocked(api.projects.leave).mockResolvedValue(undefined);
		const refresh = vi.spyOn(fleet, 'refresh').mockResolvedValue();
		render(MembersTab, { project: viewed });
		await userEvent.click(screen.getByRole('button', { name: 'Leave workshop' }));
		expect((await confirmed(true)).title).toBe('Leave Apothecary?');
		await waitFor(() => expect(goto).toHaveBeenCalledWith('/w'));
		expect(api.projects.leave).toHaveBeenCalledWith('p1');
		expect(refresh).toHaveBeenCalled();
		expect(toastTexts()).toContain('You left Apothecary');
	});

	it('stays when leaving is cancelled, and toasts a refusal', async () => {
		render(MembersTab, { project: viewed });
		await userEvent.click(screen.getByRole('button', { name: 'Leave workshop' }));
		await confirmed(false);
		expect(api.projects.leave).not.toHaveBeenCalled();

		vi.mocked(api.projects.leave).mockRejectedValue(new Error('nope'));
		await userEvent.click(screen.getByRole('button', { name: 'Leave workshop' }));
		await confirmed(true);
		await waitFor(() => expect(toastTexts()).toContain('nope'));
		expect(goto).not.toHaveBeenCalled();
	});

	it('offers the owner no way to leave', async () => {
		render(MembersTab, { project: owned });
		await waitFor(() => expect(rows()).toHaveLength(3));
		expect(screen.queryByRole('button', { name: 'Leave workshop' })).toBeNull();
	});
});
