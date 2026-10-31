import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { aComment, anAttachment, aShownFile } from '$test/fixtures/review';
import { text } from '$test/helpers';
import CommentList from './CommentList.svelte';

vi.mock('$lib/api', () => ({
	api: { files: { attachmentUrl: (id: string) => `/api/attachments/${id}` } }
}));

const plan = aShownFile({ path: 'plan.md' });
const shot = aShownFile({ path: 'shot.png', kind: 'image' });
const onPlan = aComment({ id: 'a', path: 'plan.md', text: 'On the plan' });
const onShot = aComment({ id: 'b', path: 'shot.png', text: 'On the shot' });
const props = {
	comments: [onPlan, onShot],
	files: [plan, shot],
	current: 'plan.md',
	onopen: vi.fn()
};
const allFiles = () => screen.getByRole('radio', { name: /All files/ });

describe('CommentList', () => {
	it('shows the open file’s comments', () => {
		render(CommentList, props);
		expect(screen.getByText('On the plan')).toBeInTheDocument();
		expect(screen.queryByText('On the shot')).not.toBeInTheDocument();
	});

	it('shows every file’s comments, grouped in file order, once toggled', async () => {
		const comments = [onShot, onPlan];
		render(CommentList, { ...props, comments });

		await userEvent.click(allFiles());

		const lists = screen.getAllByRole('list');
		expect(lists).toHaveLength(2);
		expect(within(lists[0]).getByText('On the plan')).toBeInTheDocument();
		expect(within(lists[1]).getByText('On the shot')).toBeInTheDocument();
		expect(screen.getByTitle('shot.png')).toHaveTextContent('shot.png');
	});

	it('goes back to the open file', async () => {
		render(CommentList, { ...props, all: true });
		await userEvent.click(screen.getByRole('radio', { name: 'This file' }));
		expect(screen.queryByText('On the shot')).not.toBeInTheDocument();
	});

	it('counts every comment on the toggle', () => {
		render(CommentList, props);
		expect(allFiles()).toHaveTextContent('All files2');
	});

	it('leaves the file with no comments out of the grouped list', () => {
		const third = aShownFile({ path: 'empty.md' });
		render(CommentList, { ...props, files: [plan, third, shot], all: true });
		expect(screen.queryByTitle('empty.md')).not.toBeInTheDocument();
		expect(screen.getAllByRole('list')).toHaveLength(2);
	});

	it('offers no toggle for a single file', () => {
		render(CommentList, { ...props, files: [plan], comments: [onPlan] });
		expect(screen.queryByRole('radiogroup')).not.toBeInTheDocument();
	});

	it('says what its caller says while the open file has none', () => {
		render(CommentList, { ...props, comments: [onShot], empty: text('Nothing here yet') });
		expect(screen.getByText('Nothing here yet')).toBeInTheDocument();
	});

	it('says there are none anywhere on the all-files list', () => {
		render(CommentList, { ...props, comments: [], all: true });
		expect(screen.getByText('No comments yet.')).toBeInTheDocument();
	});

	it.each<[string, Partial<typeof onPlan>, string]>([
		['a single line', { startLine: 4, endLine: 4 }, 'L4'],
		['a stretch of lines', { startLine: 4, endLine: 9 }, 'L4–9'],
		['a section', { startLine: 4, endLine: 9, heading: 'Risks' }, '§ Risks · L4–9'],
		['the whole file', {}, 'whole file']
	])('says where a comment on %s sits', (_, over, want) => {
		render(CommentList, { ...props, comments: [aComment(over)] });
		expect(screen.getByRole('button', { name: want })).toBeInTheDocument();
	});

	it('says a comment on an image is on the whole image', () => {
		render(CommentList, { ...props, current: 'shot.png' });
		expect(screen.getByRole('button', { name: 'whole image' })).toBeInTheDocument();
	});

	it('shows a quote in full up to its limit, cut after it', () => {
		const comments = [
			aComment({ id: 'a', quote: 'x'.repeat(120) }),
			aComment({ id: 'b', quote: 'y'.repeat(121) })
		];
		render(CommentList, { ...props, comments });
		expect(screen.getByText('x'.repeat(120))).toBeInTheDocument();
		expect(screen.getByText('y'.repeat(120) + '…')).toBeInTheDocument();
	});

	it('opens the comment clicked', async () => {
		const onopen = vi.fn();
		render(CommentList, { ...props, onopen });
		await userEvent.click(screen.getByRole('button', { name: 'whole file' }));
		expect(onopen).toHaveBeenCalledExactlyOnceWith(onPlan);
	});

	it('says what a click will do: scroll here, or open the other file', () => {
		render(CommentList, { ...props, all: true });
		expect(screen.getByRole('button', { name: 'whole file' })).toHaveAttribute(
			'title',
			'Show it in the file'
		);
		expect(screen.getByRole('button', { name: 'whole image' })).toHaveAttribute(
			'title',
			'Open shot.png here'
		);
	});

	it('is read-only without edit and delete callbacks', () => {
		render(CommentList, props);
		expect(screen.queryByRole('button', { name: 'Edit' })).not.toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Delete comment' })).not.toBeInTheDocument();
	});

	it('hands a draft’s comment to edit and delete', async () => {
		const onedit = vi.fn();
		const onremove = vi.fn();
		render(CommentList, { ...props, onedit, onremove });

		await userEvent.click(screen.getByRole('button', { name: 'Edit' }));
		await userEvent.click(screen.getByRole('button', { name: 'Delete comment' }));

		expect(onedit).toHaveBeenCalledExactlyOnceWith(onPlan);
		expect(onremove).toHaveBeenCalledExactlyOnceWith(onPlan);
	});

	it('lights the comment hovered and lets go when the pointer leaves', async () => {
		render(CommentList, props);
		const card = screen.getByRole('listitem');

		await userEvent.hover(card);
		expect(card).toHaveClass('hot');

		await userEvent.unhover(card);
		expect(card).not.toHaveClass('hot');
	});

	it('names a draft comment’s live attachments', () => {
		const files = [new File(['x'], 'a.txt'), new File(['y'], 'b.txt')];
		render(CommentList, { ...props, comments: [aComment({ files })] });
		expect(screen.getByText(/a\.txt, b\.txt/)).toBeInTheDocument();
	});

	describe('a submitted comment’s attachments', () => {
		const attachments = new Map([
			['doc', anAttachment({ id: 'doc', filename: 'notes.txt' })],
			['pic', anAttachment({ id: 'pic', filename: 'pic.png', mime: 'image/png' })]
		]);
		const comments = [aComment({ attachmentIds: ['doc', 'pic', 'gone'] })];

		it('links each to its file', () => {
			render(CommentList, { ...props, comments, attachments });
			expect(screen.getByRole('link', { name: /notes\.txt/ })).toHaveAttribute(
				'href',
				'/api/attachments/doc'
			);
		});

		it('thumbnails an image, and only an image', () => {
			const { container } = render(CommentList, { ...props, comments, attachments });
			const thumbs = [...container.querySelectorAll('img')];
			expect(thumbs.map((i) => i.getAttribute('src'))).toEqual(['/api/attachments/pic']);
		});

		it('names an attachment that is gone, without a link', () => {
			render(CommentList, { ...props, comments, attachments });
			expect(screen.getByText('gone')).toBeInTheDocument();
			expect(screen.getByText('not found')).toBeInTheDocument();
			expect(screen.getAllByRole('link')).toHaveLength(2);
		});
	});
});
