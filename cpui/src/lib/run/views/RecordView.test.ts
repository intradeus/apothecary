import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { fmtClock } from '$lib/core/format';
import { to } from '$lib/core/urls';
import { AT, anAttachment, aSnapshot, aViewStore } from '$test/fixtures/run-views';
import RecordView from './RecordView.svelte';

vi.mock('$lib/api', () => ({
	api: {
		files: {
			snapshotUrl: (id: string, dl = false) => `/snap/${id}${dl ? '?download' : ''}`,
			attachmentUrl: (id: string, dl = false) => `/att/${id}${dl ? '?download' : ''}`
		}
	}
}));

const kind = (name: RegExp) => screen.getByRole('radio', { name });
const fileLink = (name: string) => screen.getByRole('link', { name });
/** The table row a file sits on. */
const rowOf = (name: string) => within(fileLink(name).parentElement!);

function show(over: Parameters<typeof aViewStore>[1] = {}) {
	render(RecordView, { rs: aViewStore(undefined, over) });
}

describe('RecordView', () => {
	describe('states', () => {
		it('says the record is empty, and what lands on it', () => {
			show();
			expect(screen.getByText(/^Nothing on the record yet\./)).toBeInTheDocument();
			expect(screen.queryByText('Name')).not.toBeInTheDocument();
		});

		it('promises the record outlives the workspace', () => {
			show();
			expect(screen.getByText('Kept after the workspace is destroyed')).toBeInTheDocument();
		});

		it('tells an empty filter from an empty record', async () => {
			show({ snapshots: [aSnapshot()] });
			await userEvent.click(kind(/^Attachments/));
			expect(screen.getByText('No files of that kind.')).toBeInTheDocument();
		});
	});

	describe('rows', () => {
		it('merges snapshots and attachments, oldest first', () => {
			show({
				snapshots: [
					aSnapshot({ id: 's2', filename: 'late.md', createdAt: '2026-10-31T12:00:03Z' }),
					aSnapshot({ id: 's1', filename: 'early.md', createdAt: '2026-10-31T12:00:01Z' })
				],
				attachments: [anAttachment({ filename: 'middle.pdf', createdAt: '2026-10-31T12:00:02Z' })]
			});
			const names = screen
				.getAllByRole('link', { name: /^\S+\.(md|pdf)$/ })
				.map((a) => a.textContent);
			expect(names).toEqual(['early.md', 'middle.pdf', 'late.md']);
		});

		it('names an output by its role, and its stage attempt', () => {
			show({ snapshots: [aSnapshot({ name: 'plan', stageId: 'build', iteration: 2 })] });
			expect(rowOf('plan.md').getByText('plan')).toBeInTheDocument();
			expect(rowOf('plan.md').getByText('build · attempt 2')).toBeInTheDocument();
		});

		it('calls an unnamed output an output', () => {
			show({ snapshots: [aSnapshot({ name: undefined })] });
			expect(rowOf('plan.md').getByText('output')).toBeInTheDocument();
		});

		it('labels a log as a log', () => {
			show({ snapshots: [aSnapshot({ kind: 'log', filename: 'build.log' })] });
			expect(rowOf('build.log').getByText('log')).toBeInTheDocument();
		});

		it('links a file a ping showed back to that ping', () => {
			show({ snapshots: [aSnapshot({ kind: 'ping_file', pingId: 'p1' })] });
			expect(rowOf('plan.md').getByText('ping file')).toBeInTheDocument();
			expect(rowOf('plan.md').getByRole('link', { name: 'shown in a ping' })).toHaveAttribute(
				'href',
				to.ping('p1')
			);
		});

		it('leaves a source without a ping unlinked', () => {
			show({ snapshots: [aSnapshot()] });
			expect(screen.queryByRole('link', { name: 'build · attempt 2' })).not.toBeInTheDocument();
		});

		it('says an attachment came with the goal', () => {
			show({ attachments: [anAttachment()] });
			expect(rowOf('brief.pdf').getByText('attachment')).toBeInTheDocument();
			expect(rowOf('brief.pdf').getByText('run goal')).toBeInTheDocument();
		});

		it('links an attachment that came with an answer to its ping', () => {
			show({ attachments: [anAttachment({ pingId: 'p1' })] });
			expect(rowOf('brief.pdf').getByRole('link', { name: 'ping answer' })).toHaveAttribute(
				'href',
				to.ping('p1')
			);
		});

		it('states size and time', () => {
			show({ snapshots: [aSnapshot({ size: 2048 })] });
			expect(rowOf('plan.md').getByText('2 KB')).toBeInTheDocument();
			expect(rowOf('plan.md').getByText(fmtClock(AT))).toBeInTheDocument();
		});

		it('shows a dash for a time it does not have', () => {
			show({ snapshots: [aSnapshot({ createdAt: '' })] });
			expect(rowOf('plan.md').getByText('–')).toBeInTheDocument();
		});

		it.each([
			['output', () => aSnapshot(), 'tint-accent'],
			['log', () => aSnapshot({ kind: 'log' }), 'tint-accent-2']
		])('tints a %s chip by its kind', (_, snapshot, tint) => {
			show({ snapshots: [snapshot()] });
			expect(rowOf('plan.md').getByText(/^(plan|log)$/)).toHaveClass(tint);
		});

		it('tints an attachment chip by its kind', () => {
			show({ attachments: [anAttachment()] });
			expect(rowOf('brief.pdf').getByText('attachment')).toHaveClass('tint-brand');
		});
	});

	describe('getting at a file', () => {
		it('opens and downloads a snapshot from the snapshot store', () => {
			show({ snapshots: [aSnapshot({ id: 's1' })] });

			expect(fileLink('plan.md')).toHaveAttribute('href', '/snap/s1');
			expect(screen.getByRole('link', { name: 'Open' })).toHaveAttribute('href', '/snap/s1');
			expect(screen.getByRole('link', { name: 'Download plan.md' })).toHaveAttribute(
				'href',
				'/snap/s1?download'
			);
		});

		it('opens and downloads an attachment from the attachment store', () => {
			show({ attachments: [anAttachment({ id: 'a1' })] });

			expect(fileLink('brief.pdf')).toHaveAttribute('href', '/att/a1');
			expect(screen.getByRole('link', { name: 'Download brief.pdf' })).toHaveAttribute(
				'href',
				'/att/a1?download'
			);
		});

		it('opens files in a new tab', () => {
			show({ snapshots: [aSnapshot()] });
			expect(fileLink('plan.md')).toHaveAttribute('target', '_blank');
			expect(screen.getByRole('link', { name: 'Open' })).toHaveAttribute('target', '_blank');
		});
	});

	describe('the kind filter', () => {
		const everything = {
			snapshots: [
				aSnapshot({ id: 's1', filename: 'plan.md' }),
				aSnapshot({ id: 's2', kind: 'ping_file' as const, filename: 'shown.md' }),
				aSnapshot({ id: 's3', kind: 'log' as const, filename: 'build.log' })
			],
			attachments: [anAttachment({ filename: 'brief.pdf' })]
		};
		const shown = () =>
			screen.getAllByRole('link', { name: /^\S+\.(md|log|pdf)$/ }).map((a) => a.textContent);

		it('counts each kind, a ping’s file as an output', () => {
			show(everything);
			expect(kind(/^All/)).toHaveTextContent('All4');
			expect(kind(/^Outputs/)).toHaveTextContent('Outputs2');
			expect(kind(/^Logs/)).toHaveTextContent('Logs1');
			expect(kind(/^Attachments/)).toHaveTextContent('Attachments1');
		});

		it.each([
			[/^Outputs/, ['plan.md', 'shown.md']],
			[/^Logs/, ['build.log']],
			[/^Attachments/, ['brief.pdf']]
		])('shows the %s alone', async (name, files) => {
			show(everything);
			await userEvent.click(kind(name));
			expect(shown()).toEqual(files);
		});

		it('comes back to all', async () => {
			show(everything);
			await userEvent.click(kind(/^Logs/));
			await userEvent.click(kind(/^All/));
			expect(shown()).toHaveLength(4);
		});

		it('follows a file landing while the view is open', async () => {
			const rs = aViewStore(undefined, { snapshots: [aSnapshot()] });
			render(RecordView, { rs });

			rs.snapshots = [...rs.snapshots, aSnapshot({ id: 's9', filename: 'new.md' })];

			expect(await screen.findByRole('link', { name: 'new.md' })).toBeInTheDocument();
			expect(kind(/^All/)).toHaveTextContent('All2');
		});
	});
});
