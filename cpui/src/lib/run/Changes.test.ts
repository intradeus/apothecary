import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { FileDiff, WorkspaceChanges } from '$lib/domain';
import { aChangedFile, aFileDiff, someChanges } from '$test/fixtures/review';
import { deferred } from '$test/helpers';
import Changes from './Changes.svelte';

vi.mock('$lib/api', () => ({ api: { workspace: { changes: vi.fn(), diff: vi.fn() } } }));

const changes = vi.mocked(api.workspace.changes);
const diff = vi.mocked(api.workspace.diff);
const base = { runId: 'r1', active: true };
const app = aChangedFile({ path: 'src/app.ts', additions: 3, deletions: 1 });
const lib = aChangedFile({ path: 'src/lib.ts', status: 'A', additions: 10, deletions: 2 });
const two = () => someChanges({ files: [app, lib] });
const row = (path: string) => screen.getByTitle(path).closest('button')!;

/** Mounted, its branch read and the first diff in. */
async function mount(over: Partial<WorkspaceChanges> = {}, props = {}) {
	changes.mockResolvedValue(someChanges({ files: [app, lib], ...over }));
	diff.mockResolvedValue(aFileDiff());
	const view = render(Changes, { ...base, ...props });
	await waitFor(() => expect(changes).toHaveBeenCalled());
	return view;
}

describe('Changes', () => {
	beforeEach(() => vi.resetAllMocks());

	describe('the branch', () => {
		it('says it is reading until the changes arrive', () => {
			changes.mockReturnValue(deferred<WorkspaceChanges>().promise);
			render(Changes, base);
			expect(screen.getByRole('status')).toHaveTextContent('Reading the branch…');
		});

		it('says changes are unavailable, with the cause folded', async () => {
			changes.mockRejectedValue(new Error('no workspace for this run'));
			render(Changes, base);
			expect(await screen.findByRole('alert')).toHaveTextContent('Changes are unavailable.');
			expect(screen.getByText('no workspace for this run')).toBeInTheDocument();
		});

		it('tries again on Retry', async () => {
			changes.mockRejectedValueOnce(new Error('no workspace'));
			changes.mockResolvedValue(someChanges());
			render(Changes, base);

			await userEvent.click(await screen.findByRole('button', { name: /Retry/ }));

			expect(await screen.findByText('No changes yet.')).toBeInTheDocument();
			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
		});

		it('says so when the run changed nothing', async () => {
			changes.mockResolvedValue(someChanges());
			render(Changes, base);
			expect(await screen.findByText('No changes yet.')).toBeInTheDocument();
			expect(screen.getByText('0 files')).toBeInTheDocument();
			expect(diff).not.toHaveBeenCalled();
		});

		it('asks for the run and repo it was given', async () => {
			await mount({}, { runId: 'r9', repo: 'api' });
			await screen.findByText('new line', { exact: false });
			expect(changes).toHaveBeenCalledWith('r9', 'api');
			expect(diff).toHaveBeenCalledWith('r9', 'src/app.ts', 'api');
		});

		it('sums up the branch against its source', async () => {
			await mount();
			expect(await screen.findByText('main')).toBeInTheDocument();
			expect(screen.getByText('fork a1b2c3d')).toBeInTheDocument();
			expect(screen.getByText('2 files')).toBeInTheDocument();
			expect(screen.getByText('+13')).toBeInTheDocument();
			expect(screen.getByText('−3')).toBeInTheDocument();
		});

		it('falls back on HEAD, and shows no fork it does not know', async () => {
			await mount({ base: '', mergeBase: '' });
			expect(await screen.findByText('HEAD')).toBeInTheDocument();
			expect(screen.queryByText(/^fork/)).not.toBeInTheDocument();
		});

		it('reports how many files changed', async () => {
			const oncount = vi.fn();
			await mount({}, { oncount });
			await waitFor(() => expect(oncount).toHaveBeenCalledExactlyOnceWith(2));
		});

		it('shows nothing about commits when there are none', async () => {
			await mount();
			await screen.findByText('main');
			expect(screen.queryByRole('button', { name: /commit/ })).not.toBeInTheDocument();
		});

		it('folds the commits away until asked', async () => {
			const commits = [{ sha: '0123456789abcdef', subject: 'Add the key' }];
			await mount({ commits });
			const toggle = await screen.findByRole('button', { name: /1 commit/ });
			expect(toggle).toHaveAttribute('aria-expanded', 'false');
			expect(screen.queryByText('Add the key', { exact: false })).not.toBeInTheDocument();

			await userEvent.click(toggle);

			expect(toggle).toHaveAttribute('aria-expanded', 'true');
			expect(screen.getByText('Add the key', { exact: false })).toHaveTextContent(
				'0123456 Add the key'
			);
		});
	});

	describe('the files', () => {
		it.each([
			['A', 'added', 'A', 'hue-success'],
			['M', 'modified', 'M', 'hue-warning'],
			['D', 'deleted', 'D', 'hue-danger'],
			['R', 'renamed', 'R', 'hue-accent'],
			['C', 'copied', 'C', 'hue-accent-2'],
			['?', 'new, untracked', 'U', 'hue-muted']
		] as const)('marks status %s as %s', async (status, title, shown, hue) => {
			await mount({ files: [aChangedFile({ status })] });
			const mark = await screen.findByTitle(title);
			expect(mark).toHaveTextContent(shown);
			expect(mark).toHaveClass(hue);
		});

		it('shows a file’s line counts, or bin', async () => {
			const png = aChangedFile({ path: 'a.png', binary: true });
			await mount({ files: [app, png] });
			await screen.findByTitle('a.png');
			expect(row('src/app.ts')).toHaveTextContent('+3 −1');
			expect(row('a.png')).toHaveTextContent('bin');
			expect(row('a.png')).not.toHaveTextContent('+3');
		});

		it('flags a file not committed yet, and only it', async () => {
			const wip = aChangedFile({ path: 'wip.ts', committed: false });
			await mount({ files: [app, wip] });
			await screen.findByTitle('wip.ts');
			expect(screen.getAllByTitle('Not committed yet')).toHaveLength(1);
			expect(row('wip.ts')).toContainElement(screen.getByTitle('Not committed yet'));
		});

		it('names a renamed file by both its paths', async () => {
			const moved = aChangedFile({ path: 'new.ts', oldPath: 'old.ts', status: 'R' });
			await mount({ files: [moved] });
			expect(await screen.findByTitle('old.ts → new.ts')).toBeInTheDocument();
		});
	});

	describe('the diff', () => {
		it('opens the first file by itself', async () => {
			await mount();
			expect(await screen.findByText('+new line')).toBeInTheDocument();
			expect(screen.getByText('−old line')).toBeInTheDocument();
			expect(row('src/app.ts')).toHaveClass('on');
			expect(row('src/lib.ts')).not.toHaveClass('on');
		});

		it('says it is loading until the diff arrives', async () => {
			changes.mockResolvedValue(two());
			diff.mockReturnValue(deferred<FileDiff>().promise);
			render(Changes, base);
			expect(await screen.findByText('Loading diff…')).toBeInTheDocument();
		});

		it('heads the diff with the file, its status and line counts', async () => {
			await mount({ files: [aChangedFile({ path: 'a.ts', committed: false })] });
			await screen.findByText('+new line');
			expect(screen.getByText('· M · +3 −1 · uncommitted')).toBeInTheDocument();
		});

		it('heads a renamed untracked binary with what applies to it', async () => {
			const f = aChangedFile({ path: 'n.bin', oldPath: 'o.bin', status: '?', binary: true });
			await mount({ files: [f] });
			expect(await screen.findByText('· U')).toBeInTheDocument();
			expect(screen.getByText('· U').parentElement).toHaveTextContent('o.bin → n.bin');
		});

		it('says a binary file has no diff to show', async () => {
			const png = aChangedFile({ path: 'a.png', binary: true });
			await mount({ files: [png] });
			expect(await screen.findByText('Binary file')).toBeInTheDocument();
			expect(screen.queryByText('+new line')).not.toBeInTheDocument();
		});

		it('says when the diff was cut short', async () => {
			changes.mockResolvedValue(two());
			diff.mockResolvedValue(aFileDiff({ truncated: true }));
			render(Changes, base);
			expect(await screen.findByText('truncated · the rest is in the editor')).toBeInTheDocument();
		});

		it('opens the file clicked', async () => {
			await mount();
			await screen.findByText('+new line');
			diff.mockResolvedValue(aFileDiff({ diff: '@@ -1 +1 @@\n+from lib\n' }));

			await userEvent.click(row('src/lib.ts'));

			expect(await screen.findByText('+from lib')).toBeInTheDocument();
			expect(diff).toHaveBeenLastCalledWith('r1', 'src/lib.ts', '');
			expect(row('src/lib.ts')).toHaveClass('on');
		});

		it('never shows an earlier file’s diff once another is picked', async () => {
			const slow = deferred<FileDiff>();
			changes.mockResolvedValue(two());
			diff.mockReturnValueOnce(slow.promise);
			diff.mockResolvedValueOnce(aFileDiff({ diff: '@@ -1 +1 @@\n+from lib\n' }));
			render(Changes, base);
			await screen.findByTitle('src/lib.ts');

			await userEvent.click(row('src/lib.ts'));
			await screen.findByText('+from lib');
			slow.resolve(aFileDiff({ diff: '@@ -1 +1 @@\n+from app\n' }));
			await slow.promise;
			await tick();

			expect(screen.getByText('+from lib')).toBeInTheDocument();
			expect(screen.queryByText('+from app')).not.toBeInTheDocument();
		});

		it('says the diff could not be read, and retries that file', async () => {
			changes.mockResolvedValue(two());
			diff.mockRejectedValueOnce(new Error('git exploded'));
			diff.mockResolvedValue(aFileDiff());
			render(Changes, base);

			expect(await screen.findByRole('alert')).toHaveTextContent('The diff could not be read.');
			expect(screen.getByText('git exploded')).toBeInTheDocument();

			await userEvent.click(screen.getByRole('button', { name: /Retry/ }));

			expect(await screen.findByText('+new line')).toBeInTheDocument();
			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
			expect(diff).toHaveBeenLastCalledWith('r1', 'src/app.ts', '');
		});

		it('ignores an earlier file’s failure once another is picked', async () => {
			const slow = deferred<FileDiff>();
			changes.mockResolvedValue(two());
			diff.mockReturnValueOnce(slow.promise);
			diff.mockResolvedValueOnce(aFileDiff({ diff: '@@ -1 +1 @@\n+from lib\n' }));
			render(Changes, base);
			await screen.findByTitle('src/lib.ts');

			await userEvent.click(row('src/lib.ts'));
			await screen.findByText('+from lib');
			slow.reject(new Error('too late'));
			await expect(slow.promise).rejects.toThrow();
			await tick();

			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
		});

		it('drops the last file’s error when another is picked', async () => {
			changes.mockResolvedValue(two());
			diff.mockRejectedValueOnce(new Error('git exploded'));
			diff.mockReturnValue(deferred<FileDiff>().promise);
			render(Changes, base);
			await screen.findByRole('alert');

			await userEvent.click(row('src/lib.ts'));

			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
		});

		it('keeps saying it is loading when an abandoned read ends first', async () => {
			const abandoned = deferred<FileDiff>();
			changes.mockResolvedValue(two());
			diff.mockReturnValueOnce(abandoned.promise);
			diff.mockReturnValueOnce(deferred<FileDiff>().promise);
			render(Changes, base);
			await screen.findByTitle('src/lib.ts');

			await userEvent.click(row('src/lib.ts'));
			abandoned.resolve(aFileDiff());
			await abandoned.promise;
			await tick();

			expect(screen.getByRole('status')).toHaveTextContent('Loading diff…');
		});
	});

	describe('refreshing', () => {
		const refresh = () => screen.getByRole('button', { name: 'Refresh' });

		it('reads the branch and the open diff again, without a loading flash', async () => {
			await mount();
			await screen.findByText('+new line');
			const slow = deferred<FileDiff>();
			diff.mockReturnValue(slow.promise);

			await userEvent.click(refresh());

			expect(changes).toHaveBeenCalledTimes(2);
			await waitFor(() => expect(diff).toHaveBeenCalledTimes(2));
			expect(screen.getByText('+new line')).toBeInTheDocument();
			expect(screen.queryByText('Loading diff…')).not.toBeInTheDocument();
			slow.resolve(aFileDiff());
		});

		it('picks up a file that appeared', async () => {
			await mount({ files: [] });
			await screen.findByText('No changes yet.');
			changes.mockResolvedValue(someChanges({ files: [app] }));

			await userEvent.click(refresh());

			expect(await screen.findByText('+new line')).toBeInTheDocument();
		});

		it('keeps what it shows when a refresh fails', async () => {
			await mount();
			await screen.findByText('+new line');
			changes.mockRejectedValue(new Error('blip'));

			await userEvent.click(refresh());

			await waitFor(() => expect(changes).toHaveBeenCalledTimes(2));
			expect(screen.getByText('+new line')).toBeInTheDocument();
			expect(screen.queryByText('Changes are unavailable.')).not.toBeInTheDocument();
		});
	});

	describe('polling', () => {
		beforeEach(() => {
			vi.useFakeTimers();
			changes.mockResolvedValue(someChanges());
		});
		afterEach(() => {
			vi.useRealTimers();
		});
		const reads = () => changes.mock.calls.length;

		it('reads the branch every five seconds while the run works', async () => {
			render(Changes, base);
			await vi.advanceTimersByTimeAsync(4999);
			expect(reads()).toBe(1);
			await vi.advanceTimersByTimeAsync(1);
			expect(reads()).toBe(2);
			await vi.advanceTimersByTimeAsync(5000);
			expect(reads()).toBe(3);
		});

		it('stands still once the run has stopped', async () => {
			render(Changes, { ...base, active: false });
			await vi.advanceTimersByTimeAsync(20000);
			expect(reads()).toBe(1);
		});

		it('stands still while the window is off screen', async () => {
			render(Changes, { ...base, visible: false });
			await vi.advanceTimersByTimeAsync(20000);
			expect(reads()).toBe(1);
		});

		it('stands still while the tab is hidden', async () => {
			vi.spyOn(document, 'hidden', 'get').mockReturnValue(true);
			render(Changes, base);
			await vi.advanceTimersByTimeAsync(20000);
			expect(reads()).toBe(1);
		});

		it('catches up when the window comes back on screen', async () => {
			const { rerender } = render(Changes, { ...base, visible: true });
			await rerender({ visible: false });
			expect(reads()).toBe(1);

			await rerender({ visible: true });

			expect(reads()).toBe(2);
		});

		it('stops when it leaves the page', async () => {
			const { unmount } = render(Changes, base);
			unmount();
			await vi.advanceTimersByTimeAsync(20000);
			expect(reads()).toBe(1);
		});
	});
});
