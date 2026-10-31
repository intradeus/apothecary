import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { aBlock } from '$test/fixtures/review';
import Outline from './Outline.svelte';

const blocks = (...raws: string[]) => raws.map((raw) => aBlock({ raw }));
const entries = () => screen.getAllByRole('button').map((b) => b.title);

describe('Outline', () => {
	const doc = blocks('# Plan\n', 'intro', '## Risks\n', 'risk one', 'risk two', '## Next\n', 'end');

	it('lists the headings and nothing else', () => {
		render(Outline, { blocks: doc, commentBlocks: [], onjump: vi.fn() });
		expect(entries()).toEqual(['Plan', 'Risks', 'Next']);
	});

	it('says so when the file has no headings', () => {
		render(Outline, { blocks: blocks('just text'), commentBlocks: [], onjump: vi.fn() });
		expect(screen.getByText('No headings in this document.')).toBeInTheDocument();
		expect(screen.queryByRole('navigation')).not.toBeInTheDocument();
	});

	it('jumps to the heading’s block', async () => {
		const onjump = vi.fn();
		render(Outline, { blocks: doc, commentBlocks: [], onjump });
		await userEvent.click(screen.getByRole('button', { name: 'Risks' }));
		expect(onjump).toHaveBeenCalledExactlyOnceWith(2);
	});

	it('counts the comments between a heading and the next one', () => {
		render(Outline, { blocks: doc, commentBlocks: [2, 3, 4, 6], onjump: vi.fn() });
		expect(screen.getByTitle('Risks')).toHaveTextContent(/^Risks3$/);
		expect(screen.getByTitle('Next')).toHaveTextContent(/^Next1$/);
		expect(screen.getByTitle('Plan')).toHaveTextContent(/^Plan$/);
	});

	it('indents by depth, from the shallowest heading in the file', () => {
		render(Outline, {
			blocks: blocks('## Top\n', '#### Deep\n'),
			commentBlocks: [],
			onjump: vi.fn()
		});
		expect(screen.getByRole('button', { name: 'Top' })).toHaveStyle({ paddingLeft: '10px' });
		expect(screen.getByRole('button', { name: 'Deep' })).toHaveStyle({ paddingLeft: '34px' });
	});

	it.each([
		['a closing run of hashes', '## Risks ##\n', 'Risks'],
		['emphasis', '## The **big** *one*\n', 'The big one'],
		['inline code', '## The `run` loop\n', 'The run loop']
	])('reads a heading with %s as its text', (_, raw, want) => {
		render(Outline, { blocks: blocks(raw), commentBlocks: [], onjump: vi.fn() });
		expect(entries()).toEqual([want]);
	});

	it('does not take seven hashes for a heading', () => {
		render(Outline, { blocks: blocks('####### no\n'), commentBlocks: [], onjump: vi.fn() });
		expect(screen.getByText('No headings in this document.')).toBeInTheDocument();
	});

	it('lists a heading underlined with ===', () => {
		render(Outline, { blocks: blocks('Title\n==='), commentBlocks: [], onjump: vi.fn() });
		expect(entries()).toEqual(['Title']);
	});

	it('does not take a comment in an indented code block for a heading', () => {
		render(Outline, {
			blocks: blocks('    # a shell comment'),
			commentBlocks: [],
			onjump: vi.fn()
		});
		expect(screen.getByText('No headings in this document.')).toBeInTheDocument();
	});

	it('keeps the underscores of a name in a heading', () => {
		render(Outline, {
			blocks: blocks('## The `run_id` field\n'),
			commentBlocks: [],
			onjump: vi.fn()
		});
		expect(entries()).toEqual(['The run_id field']);
	});

	it('keeps a hash that is part of the last word', () => {
		render(Outline, { blocks: blocks('## Moving to C#\n'), commentBlocks: [], onjump: vi.fn() });
		expect(entries()).toEqual(['Moving to C#']);
	});
});
