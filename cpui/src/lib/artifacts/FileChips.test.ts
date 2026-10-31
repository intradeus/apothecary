import { render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import FileChips, { type StoredFile } from './FileChips.svelte';

vi.mock('$lib/api', () => ({
	api: {
		files: {
			attachmentUrl: (id: string) => `/attachments/${id}`,
			snapshotUrl: (id: string) => `/snapshots/${id}`
		}
	}
}));

const file = (over: Partial<StoredFile> = {}): StoredFile => ({
	id: 'f1',
	filename: 'plan.md',
	kind: 'attachment',
	...over
});

describe('FileChips', () => {
	it('renders nothing with no files, not even its label', () => {
		const { container } = render(FileChips, { items: [], label: 'Attached' });
		expect(container.textContent?.trim()).toBe('');
	});

	it('labels the list when told to', () => {
		render(FileChips, { items: [file()], label: 'Attached' });
		expect(screen.getByText('Attached')).toBeInTheDocument();
	});

	it.each([
		['attachment', '/attachments/f1'],
		['snapshot', '/snapshots/f1']
	] as const)('links a stored %s to its own route', (kind, href) => {
		render(FileChips, { items: [file({ kind })] });
		expect(screen.getByRole('link', { name: /plan\.md/ })).toHaveAttribute('href', href);
	});

	it('shows the size', () => {
		render(FileChips, { items: [file({ size: 2048 })] });
		expect(screen.getByText('2 KB')).toBeInTheDocument();
	});

	it('thumbnails an image from its own bytes', () => {
		const { container } = render(FileChips, {
			items: [file({ mime: 'image/png', kind: 'snapshot' })]
		});
		expect(container.querySelector('img')).toHaveAttribute('src', '/snapshots/f1');
	});

	it('gives anything else no thumbnail', () => {
		const { container } = render(FileChips, { items: [file({ mime: 'text/plain' })] });
		expect(container.querySelector('img')).toBeNull();
	});

	it('tells apart an attachment and a snapshot with the same id', () => {
		render(FileChips, {
			items: [file({ filename: 'a.md' }), file({ filename: 'b.md', kind: 'snapshot' })]
		});
		expect(screen.getAllByRole('link')).toHaveLength(2);
	});
});
