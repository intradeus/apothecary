import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { aComment, aShownFile, select, stubGeometry } from '$test/fixtures/review';
import { rect, text } from '$test/helpers';
import { readDoc, type CommentAnchor } from './reviewDoc';
import FileView from './FileView.svelte';

vi.mock('$lib/api', () => ({
	api: {
		files: {
			snapshotUrl: (id: string, download = false) =>
				`/api/snapshots/${id}${download ? '?download=1' : ''}`
		}
	}
}));

const SRC = '# Plan\n\nShip the webhook first.\n\n## Risks\n\nRetries may double charge.\n';
const doc = readDoc(SRC, 'plan.md');
const base = { file: aShownFile(), doc, comments: [], raisedAt: '2026-10-31T12:00:00Z' };
const blockOf = (s: string) => screen.getByText(s).closest<HTMLElement>('.block')!;
const textOf = (s: string) => screen.getByText(s).firstChild!;
const anchor = (over: Partial<CommentAnchor> = {}): CommentAnchor => ({
	startBlock: 1,
	startOffset: 5,
	endBlock: 1,
	endOffset: 12,
	...over
});

describe('FileView', () => {
	beforeAll(stubGeometry);

	describe('what it shows', () => {
		it('reads a file the capture missed as such, whatever it is', () => {
			render(FileView, { ...base, file: aShownFile({ captured: false, kind: 'image' }) });
			expect(screen.getByRole('alert')).toHaveTextContent(
				'plan.md was not captured when the ping was raised'
			);
			expect(screen.queryByRole('img')).not.toBeInTheDocument();
		});

		it('reads a file with no snapshot the same way', () => {
			render(FileView, { ...base, file: aShownFile({ snapshotId: undefined }) });
			expect(screen.getByRole('alert')).toHaveTextContent('was not captured');
		});

		it('shows the skeleton until a text file is read', () => {
			render(FileView, { ...base, doc: undefined });
			expect(screen.getByLabelText('Loading')).toBeInTheDocument();
		});

		it('says why a text file could not be read', () => {
			render(FileView, { ...base, doc: { blocks: [], error: 'document unavailable (404)' } });
			expect(screen.getByRole('alert')).toHaveTextContent('document unavailable (404)');
		});

		it('says so when a file is empty', () => {
			render(FileView, { ...base, doc: { blocks: [] } });
			expect(screen.getByText('The file is empty.')).toBeInTheDocument();
		});

		it('renders markdown', () => {
			render(FileView, base);
			expect(screen.getByRole('heading', { name: 'Risks' })).toBeInTheDocument();
		});

		it('shows any other file as written', () => {
			const file = aShownFile({ path: 'cfg.yaml' });
			render(FileView, {
				...base,
				file,
				doc: readDoc('# not a heading\n', 'cfg.yaml')
			});
			expect(screen.getByText('# not a heading')).toBeInTheDocument();
			expect(screen.queryByRole('heading')).not.toBeInTheDocument();
		});

		it('renders a structured file by its view, not as a listing', () => {
			const file = aShownFile({ path: 'v.yaml', type: 'verdict' });
			const src = 'verdict: REJECT\ncomments:\n  - { path: a.go, line: 3, body: wrap it }\n';
			const { container } = render(FileView, {
				...base,
				file,
				doc: readDoc(src, file.path, file.type)
			});
			expect(screen.getByRole('heading', { name: 'Rejected' })).toBeInTheDocument();
			expect(screen.getByText('wrap it')).toBeInTheDocument();
			expect(container.querySelector('.listing')).toBeNull();
		});

		it('offers anything else as a download with its size', () => {
			const file = aShownFile({ kind: 'other', snapshotId: 'bin', size: 2048 });
			render(FileView, { ...base, file });
			expect(
				screen.getByText('This file is not text or an image — it is not shown here.')
			).toBeInTheDocument();
			const link = screen.getByRole('link', { name: 'Download · 2 KB' });
			expect(link).toHaveAttribute('href', '/api/snapshots/bin?download=1');
		});

		it('leaves the size off a download that has none', () => {
			render(FileView, { ...base, file: aShownFile({ kind: 'other', size: undefined }) });
			expect(screen.getByRole('link', { name: 'Download' })).toBeInTheDocument();
		});

		it('shows an image fit to the width, a click for actual size and back', async () => {
			const file = aShownFile({ path: 'shot.png', kind: 'image', snapshotId: 'shot' });
			render(FileView, { ...base, file });
			expect(screen.getByRole('img', { name: 'shot.png' })).toHaveAttribute(
				'src',
				'/api/snapshots/shot'
			);

			const zoom = screen.getByRole('button', { name: 'Show at actual size' });
			expect(zoom).toHaveAttribute('aria-pressed', 'false');

			await userEvent.click(zoom);
			expect(screen.getByRole('button', { name: 'Fit to the width' })).toHaveAttribute(
				'aria-pressed',
				'true'
			);

			await userEvent.click(zoom);
			expect(screen.getByRole('button', { name: 'Show at actual size' })).toBeInTheDocument();
		});

		it('puts the picker above the head and the actions in it', () => {
			render(FileView, { ...base, picker: text('the picker'), actions: text('the action') });
			expect(screen.getByText('the picker')).toBeInTheDocument();
			expect(screen.getByText('the action')).toBeInTheDocument();
		});

		it('says a reviewed file is as it was reviewed', () => {
			render(FileView, { ...base, reviewed: true });
			expect(screen.getByText(/as reviewed/)).toBeInTheDocument();
		});
	});

	describe('comments on the text', () => {
		it('tints the blocks a comment’s anchor covers', () => {
			const comments = [
				aComment({ startLine: 3, endLine: 3, anchor: anchor({ startBlock: 1, endBlock: 2 }) })
			];
			render(FileView, { ...base, comments });
			expect(blockOf('Plan')).not.toHaveClass('commented');
			expect(blockOf('Ship the webhook first.')).toHaveClass('commented');
			expect(blockOf('Risks')).toHaveClass('commented');
			expect(blockOf('Retries may double charge.')).not.toHaveClass('commented');
		});

		it('finds a sent comment again by its quote, not just its lines', async () => {
			const comments = [aComment({ quote: 'double charge', startLine: 1, endLine: 7 })];
			render(FileView, { ...base, comments });
			await waitFor(() => expect(blockOf('Retries may double charge.')).toHaveClass('commented'));
			expect(blockOf('Plan')).not.toHaveClass('commented');
		});

		it('finds it once the text arrives, not only at mount', async () => {
			const comments = [aComment({ quote: 'double charge', startLine: 1, endLine: 7 })];
			const { rerender } = render(FileView, { ...base, comments, doc: undefined });
			await rerender({ doc });
			await waitFor(() => expect(blockOf('Plan')).not.toHaveClass('commented'));
		});

		it('falls back on the comment’s lines when its quote is gone', () => {
			const comments = [aComment({ quote: 'rewritten since', startLine: 3, endLine: 3 })];
			render(FileView, { ...base, comments });
			expect(blockOf('Ship the webhook first.')).toHaveClass('commented');
			expect(blockOf('Plan')).not.toHaveClass('commented');
		});

		it('tints no block for a whole-file comment', () => {
			const { container } = render(FileView, { ...base, comments: [aComment()] });
			expect(container.querySelector('.block.commented')).toBeNull();
		});

		it('lights the active comment’s blocks', () => {
			const comments = [aComment({ startLine: 3, endLine: 3 })];
			render(FileView, { ...base, comments, active: 'c1' });
			expect(blockOf('Ship the webhook first.')).toHaveClass('active');
			expect(blockOf('Plan')).not.toHaveClass('active');
		});

		it('lights the blocks a comment is being written on', () => {
			render(FileView, { ...base, pending: anchor({ startBlock: 2, endBlock: 3 }) });
			expect(blockOf('Risks')).toHaveClass('pending');
			expect(blockOf('Retries may double charge.')).toHaveClass('pending');
			expect(blockOf('Plan')).not.toHaveClass('pending');
		});

		it('lights the file itself for an active whole-file comment', () => {
			const { container } = render(FileView, { ...base, comments: [aComment()], active: 'c1' });
			expect(container.querySelector('.paper')).toHaveClass('flash');
		});

		it('does not light the file for a comment on its lines', () => {
			const comments = [aComment({ startLine: 3, endLine: 3 })];
			const { container } = render(FileView, { ...base, comments, active: 'c1' });
			expect(container.querySelector('.paper')).not.toHaveClass('flash');
		});
	});

	describe('a selection', () => {
		it('knows whether a selection lies in the text', () => {
			const { component } = render(FileView, base);
			const outside = document.createElement('p');
			outside.textContent = 'elsewhere';
			document.body.append(outside);

			expect(component.holds(select(textOf('Risks'), 0, textOf('Risks'), 3))).toBe(true);
			expect(component.holds(select(outside.firstChild!, 0, outside.firstChild!, 3))).toBe(false);

			outside.remove();
		});

		it('holds no selection in a file with nothing to select', () => {
			const { component } = render(FileView, { ...base, doc: undefined });
			const range = document.createRange();
			range.selectNodeContents(screen.getByText('plan.md'));
			expect(component.holds(range)).toBe(false);
		});

		it('anchors a selection inside one block, with its section and lines', () => {
			const { component } = render(FileView, base);
			const t = textOf('Retries may double charge.');
			expect(component.anchorFrom(select(t, 8, t, 11))).toEqual({
				anchor: { startBlock: 3, startOffset: 8, endBlock: 3, endOffset: 11 },
				heading: 'Risks',
				startLine: 7,
				endLine: 7
			});
		});

		it('anchors a selection across blocks, in the first block’s section', () => {
			const { component } = render(FileView, base);
			const range = select(
				textOf('Ship the webhook first.'),
				5,
				textOf('Retries may double charge.'),
				7
			);
			expect(component.anchorFrom(range)).toEqual({
				anchor: { startBlock: 1, startOffset: 5, endBlock: 3, endOffset: 7 },
				heading: 'Plan',
				startLine: 3,
				endLine: 7
			});
		});

		it('credits the text covered when a drag ends at the head of the next block', () => {
			const { component } = render(FileView, base);
			const first = 'Ship the webhook first.';
			const range = select(textOf(first), 5, textOf('Risks'), 0);
			const endOffset = blockOf(first).textContent!.length;
			expect(component.anchorFrom(range)).toEqual({
				anchor: { startBlock: 1, startOffset: 5, endBlock: 1, endOffset },
				heading: 'Plan',
				startLine: 3,
				endLine: 3
			});
		});

		it('anchors nothing outside the blocks', () => {
			const { component } = render(FileView, base);
			const range = document.createRange();
			range.selectNodeContents(screen.getByText('plan.md'));
			expect(component.anchorFrom(range)).toBeNull();
		});
	});

	describe('finding a comment', () => {
		const lineComment = aComment({ id: 'l', startLine: 7, endLine: 7 });
		// Its anchor says block 2; its lines alone would say block 1.
		const anchored = aComment({
			id: 'a',
			startLine: 3,
			endLine: 3,
			anchor: anchor({ startBlock: 2, endBlock: 2, startOffset: 0, endOffset: 4 })
		});
		const whole = aComment({ id: 'w' });

		it('names the block each line comment starts in, leaving whole-file ones out', () => {
			const comments = [lineComment, anchored, whole];
			const { component } = render(FileView, { ...base, comments });
			expect(component.commentBlocks()).toEqual([3, 2]);
		});

		it('scrolls a line comment’s block to the middle', () => {
			const scroll = vi.spyOn(Element.prototype, 'scrollIntoView');
			const { component } = render(FileView, base);

			component.reveal(lineComment);

			expect(scroll).toHaveBeenCalledExactlyOnceWith({ behavior: 'smooth', block: 'center' });
			expect(scroll.mock.contexts[0]).toBe(blockOf('Retries may double charge.'));
		});

		it('scrolls a whole-file comment to the head of the file', () => {
			const scroll = vi.spyOn(Element.prototype, 'scrollTo');
			const { component, container } = render(FileView, base);

			component.reveal(whole);

			expect(scroll).toHaveBeenCalledExactlyOnceWith({ top: 0, behavior: 'smooth' });
			expect(scroll.mock.contexts[0]).toBe(container.querySelector('.doc-scroll'));
		});

		it('jumps to a block by its index', () => {
			const scroll = vi.spyOn(Element.prototype, 'scrollIntoView');
			const { component } = render(FileView, base);

			component.jump(2);

			expect(scroll).toHaveBeenCalledExactlyOnceWith({ behavior: 'smooth', block: 'start' });
			expect(scroll.mock.contexts[0]).toBe(blockOf('Risks'));
		});

		it('places an anchored comment by its text', () => {
			const { component } = render(FileView, base);
			expect(component.rectOf(anchored)).toMatchObject({ left: 100, top: 200 });
		});

		it('places a comment without an anchor by its block', () => {
			const { component } = render(FileView, base);
			const block = blockOf('Retries may double charge.');
			const at = rect(1, 2, 3, 4);
			vi.spyOn(block, 'getBoundingClientRect').mockReturnValue(at);
			expect(component.rectOf(lineComment)).toBe(at);
		});

		it('places a whole-file comment by the file', () => {
			const { component, container } = render(FileView, base);
			const paper = container.querySelector('.paper')!;
			const at = rect(5, 6, 7, 8);
			vi.spyOn(paper, 'getBoundingClientRect').mockReturnValue(at);
			expect(component.rectOf(whole)).toBe(at);
		});

		it('has no place for a comment in a file not on screen', () => {
			const { component } = render(FileView, { ...base, doc: undefined });
			expect(component.rectOf(whole)).toBeNull();
			expect(component.rectOf(lineComment)).toBeNull();
		});
	});

	describe('the exact-text highlight', () => {
		class Highlight {
			ranges: Range[];
			constructor(...ranges: Range[]) {
				this.ranges = ranges;
			}
		}
		const installed = () => {
			const highlights = new Map<string, Highlight>();
			const g = globalThis as unknown as Record<string, unknown>;
			g.CSS = { highlights };
			g.Highlight = Highlight;
			return highlights;
		};
		const painted = (h: Map<string, Highlight>, name: string) =>
			h.get(name)?.ranges.map((r) => r.toString().trim());

		afterEach(() => {
			const g = globalThis as unknown as Record<string, unknown>;
			delete g.CSS;
			delete g.Highlight;
		});

		it('paints the selection a comment is being written on, block by block', () => {
			const highlights = installed();
			const pending = anchor({ startBlock: 1, startOffset: 9, endBlock: 2, endOffset: 3 });
			render(FileView, { ...base, pending });
			expect(painted(highlights, 'apo-doc-pending')).toEqual(['webhook first.', 'Ris']);
		});

		it('paints the active comment’s exact text', () => {
			const highlights = installed();
			const comments = [
				aComment({
					startLine: 3,
					endLine: 3,
					anchor: anchor({ startOffset: 9, endOffset: 16 })
				})
			];
			render(FileView, { ...base, comments, active: 'c1' });
			expect(painted(highlights, 'apo-doc-active')).toEqual(['webhook']);
		});

		it('paints a sent comment’s quote, found again', async () => {
			const highlights = installed();
			const comments = [aComment({ quote: 'double charge', startLine: 7, endLine: 7 })];
			render(FileView, { ...base, comments, active: 'c1' });
			await waitFor(() => expect(painted(highlights, 'apo-doc-active')).toEqual(['double charge']));
		});

		it('paints nothing for a whole-file comment', () => {
			const highlights = installed();
			render(FileView, { ...base, comments: [aComment()], active: 'c1' });
			expect(highlights.has('apo-doc-active')).toBe(false);
		});

		it('clears what it painted when the selection goes and when it leaves', async () => {
			const highlights = installed();
			const { rerender, unmount } = render(FileView, { ...base, pending: anchor() });
			expect(highlights.has('apo-doc-pending')).toBe(true);

			await rerender({ pending: null });
			expect(highlights.has('apo-doc-pending')).toBe(false);

			await rerender({ pending: anchor() });
			unmount();
			expect(highlights.has('apo-doc-pending')).toBe(false);
		});
	});

	describe('the scroll position', () => {
		const scroller = (c: Element) => c.querySelector<HTMLElement>('.doc-scroll')!;

		it('lands where the file was left', async () => {
			const scrolls = new Map([['plan.md', 240]]);
			const { container } = render(FileView, { ...base, scrolls });
			await waitFor(() => expect(scroller(container).scrollTop).toBe(240));
		});

		it('waits for a text file to be read before going back', async () => {
			const scrolls = new Map([['plan.md', 240]]);
			const { container, rerender } = render(FileView, { ...base, doc: undefined, scrolls });
			await tick();
			expect(scroller(container).scrollTop).toBe(0);

			await rerender({ doc });
			await waitFor(() => expect(scroller(container).scrollTop).toBe(240));
		});

		it('keeps the offset as the file scrolls', async () => {
			const scrolls = new Map<string, number>();
			const { container } = render(FileView, { ...base, scrolls });
			const el = scroller(container);
			el.scrollTop = 90;
			await fireEvent.scroll(el);
			expect(scrolls.get('plan.md')).toBe(90);
		});

		it('records nothing while the file has not been put back yet', async () => {
			const scrolls = new Map<string, number>();
			const { container } = render(FileView, { ...base, doc: undefined, scrolls });
			await fireEvent.scroll(scroller(container));
			expect(scrolls.size).toBe(0);
		});
	});
});
