import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { I } from '$lib/design/icons';
import FileChip from './FileChip.svelte';

describe('FileChip', () => {
	it('shows the name and a readable size', () => {
		render(FileChip, { name: 'plan.md', size: 2048 });
		expect(screen.getByText('plan.md')).toHaveAttribute('title', 'plan.md');
		expect(screen.getByText('2 KB')).toBeInTheDocument();
	});

	it('shows an empty file’s size', () => {
		render(FileChip, { name: 'empty.txt', size: 0 });
		expect(screen.getByText('0 B')).toBeInTheDocument();
	});

	it('links out to a stored file in a new tab', () => {
		render(FileChip, { name: 'plan.md', href: '/files/1' });
		const link = screen.getByRole('link', { name: /plan\.md/ });
		expect(link).toHaveAttribute('href', '/files/1');
		expect(link).toHaveAttribute('target', '_blank');
		expect(link).toHaveAttribute('rel', 'noreferrer');
	});

	it('uses the thumbnail in place of the glyph', () => {
		const { container } = render(FileChip, { name: 'a.png', thumb: '/a.png' });
		expect(container.querySelector('img')).toHaveAttribute('src', '/a.png');
		expect(screen.queryByText(I.file)).not.toBeInTheDocument();
	});

	it('uses the file glyph without a thumbnail', () => {
		render(FileChip, { name: 'a.txt' });
		expect(screen.getByText(I.file)).toBeInTheDocument();
	});

	it.each(['missing', 'refused'] as const)(
		'reads as %s: warning glyph, the reason, nothing to open',
		(state) => {
			const { container } = render(FileChip, {
				name: 'a.png',
				size: 2048,
				href: '/a.png',
				thumb: '/a.png',
				state,
				reason: 'Over the limit'
			});
			expect(screen.getByText('Over the limit')).toBeInTheDocument();
			expect(screen.queryByText('2 KB')).not.toBeInTheDocument();
			expect(screen.getByText(I.warn)).toBeInTheDocument();
			expect(screen.queryByRole('link')).not.toBeInTheDocument();
			expect(container.querySelector('img')).not.toBeInTheDocument();
			expect(container.firstElementChild).toHaveClass(state);
		}
	);

	it('offers a named remove when it has onremove', async () => {
		const onremove = vi.fn();
		render(FileChip, { name: 'plan.md', onremove });
		await userEvent.click(screen.getByRole('button', { name: 'Remove plan.md' }));
		expect(onremove).toHaveBeenCalledOnce();
	});

	it('keeps the remove on a refused file', () => {
		render(FileChip, { name: 'big.iso', state: 'refused', onremove: vi.fn() });
		expect(screen.getByRole('button', { name: 'Remove big.iso' })).toBeInTheDocument();
	});

	it('is read-only without onremove', () => {
		render(FileChip, { name: 'plan.md' });
		expect(screen.queryByRole('button')).not.toBeInTheDocument();
	});
});
