import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import type { AgentToolCall } from '$lib/domain';
import { aToolCall } from '$test/fixtures/run-components';
import ToolCalls from './ToolCalls.svelte';

const calls = (n: number, over: Partial<AgentToolCall> = {}) =>
	Array.from({ length: n }, (_, i) =>
		aToolCall({ toolCallId: `c${i}`, title: `Call ${i}`, ...over })
	);
const rowOf = (name: string | RegExp) => screen.getByRole('button', { name });

describe('ToolCalls', () => {
	describe('one call', () => {
		it.each([
			['read', 'Read', 'hue-running'],
			['edit', 'Edit', 'hue-warning'],
			['search', 'Search', 'hue-accent'],
			['execute', 'Exec', 'hue-success'],
			['exec', 'Exec', 'hue-success'],
			['delete', 'Delete', 'hue-danger'],
			['move', 'Move', 'hue-accent-2'],
			['fetch', 'Fetch', 'hue-running'],
			['think', 'Think', 'hue-muted'],
			['other', 'Tool', 'hue-muted'],
			['', 'Tool', 'hue-muted'],
			['lint', 'lint', 'hue-muted']
		])('words the kind %j as %s', (kind, label, hue) => {
			render(ToolCalls, { calls: [aToolCall({ kind })] });
			expect(screen.getByText(label)).toHaveClass(hue);
		});

		it.each([
			['completed', 'completed', 'ok'],
			['failed', 'failed', 'bad'],
			['in_progress', 'in progress', 'live'],
			['pending', 'pending', 'pending'],
			['timed_out', 'timed out', 'pending']
		])('marks the status %s', (status, word, tone) => {
			render(ToolCalls, { calls: [aToolCall({ status })] });
			expect(screen.getByRole('img', { name: word })).toHaveClass(tone);
		});

		it('says so in words while a call is in flight, and only then', () => {
			const { unmount } = render(ToolCalls, { calls: [aToolCall({ status: 'in_progress' })] });
			expect(screen.getByText('in progress')).toBeInTheDocument();
			unmount();

			render(ToolCalls, { calls: [aToolCall({ status: 'pending' })] });
			expect(screen.queryByText('in progress')).not.toBeInTheDocument();
		});

		it('shows where it acted over its title', () => {
			render(ToolCalls, {
				calls: [aToolCall({ title: 'Read the router', locations: ['src/router.ts', 'b.ts'] })]
			});

			const path = screen.getByText('src/router.ts');
			expect(path).toHaveAttribute('title', 'Read the router');
			expect(screen.queryByText('b.ts')).not.toBeInTheDocument();
		});

		it('falls back to the title, then the kind', () => {
			const { unmount } = render(ToolCalls, { calls: [aToolCall({ title: 'grep foo' })] });
			expect(screen.getByText('grep foo')).toBeInTheDocument();
			unmount();

			render(ToolCalls, { calls: [aToolCall({ title: '', kind: 'read' })] });
			expect(screen.getByText('read')).toBeInTheDocument();
		});

		it('counts the lines its diffs change, across files', () => {
			render(ToolCalls, {
				calls: [
					aToolCall({
						kind: 'edit',
						diffs: [
							{ path: 'a.ts', oldText: 'one\ntwo', newText: 'one\nTWO\nthree' },
							{ path: 'b.ts', newText: 'new' }
						]
					})
				]
			});

			expect(screen.getByText('+3')).toHaveClass('hue-success');
			expect(screen.getByText('−1')).toHaveClass('hue-danger');
		});

		it('counts nothing for a call without diffs', () => {
			render(ToolCalls, { calls: [aToolCall()] });
			expect(screen.queryByText(/^\+\d/)).not.toBeInTheDocument();
		});
	});

	describe('expanding a call', () => {
		it('starts shut and toggles', async () => {
			render(ToolCalls, { calls: [aToolCall({ input: 'rg foo' })] });
			const row = rowOf(/Read/);
			expect(row).toHaveAttribute('aria-expanded', 'false');
			expect(screen.queryByText('rg foo')).not.toBeInTheDocument();

			await userEvent.click(row);
			expect(row).toHaveAttribute('aria-expanded', 'true');
			expect(screen.getByText('rg foo')).toBeInTheDocument();

			await userEvent.click(row);
			expect(row).toHaveAttribute('aria-expanded', 'false');
			expect(screen.queryByText('rg foo')).not.toBeInTheDocument();
		});

		it('keeps each call to itself', async () => {
			render(ToolCalls, {
				calls: [
					aToolCall({ toolCallId: 'a', title: 'first', input: 'in a' }),
					aToolCall({ toolCallId: 'b', title: 'second', input: 'in b' })
				]
			});

			await userEvent.click(rowOf(/second/));

			expect(screen.getByText('in b')).toBeInTheDocument();
			expect(screen.queryByText('in a')).not.toBeInTheDocument();
		});

		it('leaves one open when another is opened', async () => {
			render(ToolCalls, {
				calls: [
					aToolCall({ toolCallId: 'a', title: 'first', input: 'in a' }),
					aToolCall({ toolCallId: 'b', title: 'second', input: 'in b' })
				]
			});

			await userEvent.click(rowOf(/first/));
			await userEvent.click(rowOf(/second/));

			expect(screen.getByText('in a')).toBeInTheDocument();
			expect(screen.getByText('in b')).toBeInTheDocument();
		});

		it('shows the input and the output', async () => {
			render(ToolCalls, {
				calls: [aToolCall({ status: 'failed', input: 'cargo test', output: 'exit 101' })]
			});
			await userEvent.click(rowOf(/Read/));

			expect(screen.getByText('input')).toBeInTheDocument();
			expect(screen.getByText('cargo test')).toBeInTheDocument();
			expect(screen.getByText('output')).toBeInTheDocument();
			expect(screen.getByText('exit 101')).toBeInTheDocument();
		});

		it('says so when there is nothing to show', async () => {
			render(ToolCalls, { calls: [aToolCall()] });
			await userEvent.click(rowOf(/Read/));

			expect(screen.getByText('no details reported')).toBeInTheDocument();
			expect(screen.queryByText('input')).not.toBeInTheDocument();
			expect(screen.queryByText('output')).not.toBeInTheDocument();
		});

		it('gives the title a line when the row shows the path', async () => {
			render(ToolCalls, {
				calls: [aToolCall({ title: 'Read the router', locations: ['src/router.ts'] })]
			});
			await userEvent.click(rowOf(/Read/));

			expect(screen.getByText('Read the router')).toBeInTheDocument();
		});

		it('does not repeat the title the row already shows', async () => {
			render(ToolCalls, { calls: [aToolCall({ title: 'Read the router' })] });
			await userEvent.click(rowOf(/Read/));

			expect(screen.getAllByText('Read the router')).toHaveLength(1);
		});

		it('shows each diff under its path', async () => {
			render(ToolCalls, {
				calls: [
					aToolCall({
						kind: 'edit',
						diffs: [{ path: 'src/a.ts', oldText: 'old line', newText: 'new line' }]
					})
				]
			});
			await userEvent.click(rowOf(/Edit/));

			expect(screen.getByText('src/a.ts')).toBeInTheDocument();
			expect(screen.getByText('−old line')).toBeInTheDocument();
			expect(screen.getByText('+new line')).toBeInTheDocument();
			expect(screen.queryByText('no details reported')).not.toBeInTheDocument();
		});

		it('cuts a long diff at 400 lines and says so', async () => {
			const newText = Array.from({ length: 500 }, (_, i) => `line ${i}`).join('\n');
			render(ToolCalls, {
				calls: [aToolCall({ kind: 'edit', diffs: [{ path: 'big.ts', newText }] })]
			});
			await userEvent.click(rowOf(/Edit/));

			expect(screen.getByText('+line 399')).toBeInTheDocument();
			expect(screen.queryByText('+line 400')).not.toBeInTheDocument();
			expect(screen.getByText('truncated · showing 400 of 500 lines')).toBeInTheDocument();
		});

		it('does not call a short diff truncated', async () => {
			render(ToolCalls, {
				calls: [aToolCall({ kind: 'edit', diffs: [{ path: 'a.ts', newText: 'x' }] })]
			});
			await userEvent.click(rowOf(/Edit/));

			expect(screen.queryByText(/truncated/)).not.toBeInTheDocument();
		});

		// The engine keeps every diff a tool call reports, one file or not.
		it('shows two edits to the same file in one call', async () => {
			render(ToolCalls, {
				calls: [
					aToolCall({
						kind: 'edit',
						diffs: [
							{ path: 'a.ts', oldText: 'one', newText: 'ONE' },
							{ path: 'a.ts', oldText: 'two', newText: 'TWO' }
						]
					})
				]
			});
			await userEvent.click(rowOf(/Edit/));

			expect(screen.getByText('+ONE')).toBeInTheDocument();
			expect(screen.getByText('+TWO')).toBeInTheDocument();
		});
	});

	describe('folding', () => {
		it('lists up to three calls', () => {
			render(ToolCalls, { calls: calls(3) });

			expect(screen.getByText('Call 2')).toBeInTheDocument();
			expect(screen.queryByText(/tool calls/)).not.toBeInTheDocument();
		});

		it('folds more than three behind a summary', () => {
			render(ToolCalls, { calls: calls(4) });

			expect(rowOf(/4 tool calls/)).toBeInTheDocument();
			expect(screen.queryByText('Call 0')).not.toBeInTheDocument();
		});

		it('counts the kinds, one chip per word', () => {
			render(ToolCalls, {
				calls: [
					...calls(2, { kind: 'read' }).map((c, i) => ({ ...c, toolCallId: `r${i}` })),
					aToolCall({ toolCallId: 'e1', kind: 'execute' }),
					aToolCall({ toolCallId: 'e2', kind: 'exec' }),
					aToolCall({ toolCallId: 'o', kind: 'lint' })
				]
			});

			expect(screen.getByText('read 2')).toHaveClass('tint-running');
			expect(screen.getByText('exec 2')).toHaveClass('tint-success');
			expect(screen.getByText('lint 1')).toHaveClass('tint-muted');
		});

		it('says how many failed, and nothing when none did', () => {
			const some = calls(5).map((c, i) => (i < 2 ? { ...c, status: 'failed' } : c));
			const { unmount } = render(ToolCalls, { calls: some });
			expect(screen.getByText('2 failed')).toBeInTheDocument();
			unmount();

			render(ToolCalls, { calls: calls(5) });
			expect(screen.queryByText(/failed$/)).not.toBeInTheDocument();
		});

		it('opens and folds again', async () => {
			render(ToolCalls, { calls: calls(4) });

			await userEvent.click(rowOf(/4 tool calls/));
			expect(screen.getByText('Call 3')).toBeInTheDocument();

			await userEvent.click(rowOf(/4 tool calls/));
			expect(screen.queryByText('Call 3')).not.toBeInTheDocument();
		});

		it('never folds a live group, and offers no way to', () => {
			render(ToolCalls, { calls: calls(6), live: true });

			expect(screen.getByText('Call 5')).toBeInTheDocument();
			expect(screen.queryByText(/tool calls/)).not.toBeInTheDocument();
		});

		it('folds once the group is no longer live', async () => {
			const { rerender } = render(ToolCalls, { calls: calls(6), live: true });

			await rerender({ live: false });

			expect(screen.queryByText('Call 5')).not.toBeInTheDocument();
			expect(rowOf(/6 tool calls/)).toBeInTheDocument();
		});

		it('takes new calls as they arrive', async () => {
			const { rerender } = render(ToolCalls, { calls: calls(1), live: true });

			await rerender({ calls: calls(2) });

			expect(screen.getByText('Call 1')).toBeInTheDocument();
		});
	});
});
