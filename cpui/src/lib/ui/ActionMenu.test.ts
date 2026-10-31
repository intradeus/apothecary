import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { rect } from '$test/helpers';
import ActionMenu from './ActionMenu.svelte';

const run = {
	gitlab: vi.fn(),
	github: vi.fn(),
	matrix: vi.fn()
};
const groups = [
	{
		title: 'Repositories',
		items: [
			{ label: 'GitLab', run: run.gitlab },
			{ label: 'GitHub', run: run.github }
		]
	},
	{ title: 'Empty', items: [] },
	{ title: 'Channels', items: [{ label: 'Matrix', run: run.matrix }] }
];
const button = () => screen.getByRole('button', { name: /New integration/ });
const setup = () => render(ActionMenu, { label: 'New integration', groups });
const open = async () => {
	setup();
	await userEvent.click(button());
};

describe('ActionMenu', () => {
	beforeEach(() => {
		Object.values(run).forEach((fn) => fn.mockClear());
	});

	it('is a closed menu button', () => {
		setup();
		expect(button()).toHaveAttribute('aria-haspopup', 'menu');
		expect(button()).toHaveAttribute('aria-expanded', 'false');
		expect(screen.queryByRole('menu')).not.toBeInTheDocument();
	});

	it('opens the kinds under it, and closes from the same button', async () => {
		await open();
		expect(button()).toHaveAttribute('aria-expanded', 'true');
		expect(screen.getAllByRole('menuitem').map((m) => m.textContent)).toEqual([
			'GitLab',
			'GitHub',
			'Matrix'
		]);

		await userEvent.click(button());
		expect(button()).toHaveAttribute('aria-expanded', 'false');
		await waitFor(() => expect(screen.queryByRole('menu')).not.toBeInTheDocument());
	});

	it('titles its groups and drops the empty ones', async () => {
		await open();
		expect(screen.getByText('Repositories')).toBeInTheDocument();
		expect(screen.getByText('Channels')).toBeInTheDocument();
		expect(screen.queryByText('Empty')).not.toBeInTheDocument();
	});

	it('draws a divider only between the groups that show', async () => {
		await open();
		expect(screen.getByRole('menu').querySelectorAll('.menu-div')).toHaveLength(1);
	});

	it('takes groups with no title', async () => {
		render(ActionMenu, {
			label: 'New integration',
			groups: [
				{ items: [{ label: 'One', run: vi.fn() }] },
				{ items: [{ label: 'Two', run: vi.fn() }] }
			]
		});
		await userEvent.click(button());
		expect(screen.getAllByRole('menuitem')).toHaveLength(2);
	});

	it('runs the kind that was picked, once, and closes', async () => {
		await open();
		await userEvent.click(screen.getByRole('menuitem', { name: 'GitHub' }));

		expect(run.github).toHaveBeenCalledOnce();
		expect(run.gitlab).not.toHaveBeenCalled();
		expect(button()).toHaveAttribute('aria-expanded', 'false');
	});

	it('closes on Escape and hands focus back to the button', async () => {
		await open();
		await userEvent.keyboard('{Escape}');

		expect(button()).toHaveAttribute('aria-expanded', 'false');
		expect(button()).toHaveFocus();
	});

	it('closes on a press outside', async () => {
		await open();
		await fireEvent.pointerDown(document.body);
		expect(button()).toHaveAttribute('aria-expanded', 'false');
	});

	it('opens from the keyboard', async () => {
		setup();
		await userEvent.tab();
		expect(button()).toHaveFocus();
		await userEvent.keyboard('{Enter}');
		expect(screen.getByRole('menu')).toBeInTheDocument();
	});

	// The list is portaled to the end of <body>: Tab alone never reaches an item.
	describe('from the keyboard', () => {
		const item = (name: string) => screen.getByRole('menuitem', { name });
		const opened = async () => {
			setup();
			await userEvent.tab();
			await userEvent.keyboard('{Enter}');
			await tick();
		};

		it('moves focus to the first item when opened from the keyboard', async () => {
			await opened();
			expect(item('GitLab')).toHaveFocus();
		});

		it('leaves the focus alone when opened with the pointer', async () => {
			await open();
			await tick();
			expect(button()).toHaveFocus();
		});

		it('walks the items with the arrows, wrapping at both ends', async () => {
			await opened();
			await userEvent.keyboard('{ArrowDown}');
			expect(item('GitHub')).toHaveFocus();

			await userEvent.keyboard('{ArrowUp}{ArrowUp}');
			expect(item('Matrix')).toHaveFocus();

			await userEvent.keyboard('{ArrowDown}');
			expect(item('GitLab')).toHaveFocus();
		});

		it('jumps to the ends with Home and End', async () => {
			await opened();
			await userEvent.keyboard('{End}');
			expect(item('Matrix')).toHaveFocus();

			await userEvent.keyboard('{Home}');
			expect(item('GitLab')).toHaveFocus();
		});

		it('runs the item it is on and hands focus back to the button', async () => {
			await opened();
			await userEvent.keyboard('{ArrowDown}{Enter}');

			expect(run.github).toHaveBeenCalledOnce();
			expect(button()).toHaveFocus();
		});

		it('closes on Tab, moving on from the button', async () => {
			await opened();
			button().after(Object.assign(document.createElement('button'), { textContent: 'Next' }));

			await userEvent.tab();

			expect(button()).toHaveAttribute('aria-expanded', 'false');
			expect(screen.getByRole('button', { name: 'Next' })).toHaveFocus();
		});
	});

	it('opens upward when it would not fit below, and settles there', async () => {
		const errors: unknown[] = [];
		const onerror = (e: ErrorEvent) => errors.push(e.error);
		window.addEventListener('error', onerror);
		const logged = vi.spyOn(console, 'error');

		setup();
		vi.spyOn(button(), 'getBoundingClientRect').mockReturnValue(
			rect(800, window.innerHeight - 60, 120, 40)
		);
		vi.spyOn(Element.prototype, 'scrollHeight', 'get').mockReturnValue(300);
		await userEvent.click(button());
		await tick();

		const menu = screen.getByRole('menu');
		await waitFor(() => expect(menu.style.bottom).toBe('66px'));
		expect(menu.style.top).toBe('');
		expect(errors).toEqual([]);
		expect(String(logged.mock.calls)).not.toContain('effect_update_depth_exceeded');
		window.removeEventListener('error', onerror);
	});
});
