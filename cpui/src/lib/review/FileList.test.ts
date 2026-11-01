import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { prefs } from '$lib/app/prefs.svelte';
import { I } from '$lib/design/icons';
import { aShownFile } from '$test/fixtures/review';
import FileList, { baseName, dirName, KIND_ICON } from './FileList.svelte';

const plan = aShownFile({ path: 'docs/plan.md' });
const shot = aShownFile({ path: 'shot.png', kind: 'image' });
const blob = aShownFile({ path: 'data.bin', kind: 'other' });
const files = [plan, shot, blob];
const props = { files, current: 'docs/plan.md', counts: {}, onpick: vi.fn() };
const row = (path: string) => screen.getByTitle(path);

describe('baseName and dirName', () => {
	it.each([
		['docs/deep/plan.md', 'plan.md', 'docs/deep'],
		['plan.md', 'plan.md', ''],
		['', '', '']
	])('split %s', (path, base, dir) => {
		expect(baseName(path)).toBe(base);
		expect(dirName(path)).toBe(dir);
	});
});

beforeEach(() => prefs.setFileLayout('list'));

describe('FileList as a list', () => {
	it('shows each file as the gate ordered them, by name and folder', () => {
		render(FileList, props);
		const nav = screen.getByRole('navigation', { name: 'Files under review' });
		expect(
			within(nav)
				.getAllByRole('button')
				.map((b) => b.title)
		).toEqual(['docs/plan.md', 'shot.png', 'data.bin']);
		expect(row('docs/plan.md')).toHaveTextContent('plan.md');
		expect(row('docs/plan.md')).toHaveTextContent('docs');
	});

	it.each([
		['text', 'docs/plan.md'],
		['image', 'shot.png'],
		['other', 'data.bin']
	] as const)('marks a file of kind %s with its glyph', (kind, path) => {
		render(FileList, props);
		expect(row(path)).toHaveTextContent(KIND_ICON[kind]);
	});

	it('marks the open file, and only it', () => {
		render(FileList, props);
		expect(row('docs/plan.md')).toHaveAttribute('aria-current', 'true');
		expect(row('shot.png')).not.toHaveAttribute('aria-current');
	});

	it('picks the file clicked', async () => {
		const onpick = vi.fn();
		render(FileList, { ...props, onpick });
		await userEvent.click(row('shot.png'));
		expect(onpick).toHaveBeenCalledExactlyOnceWith('shot.png');
	});

	it('shows how many comments a file carries, none when it has none', () => {
		render(FileList, { ...props, counts: { 'shot.png': 4 } });
		expect(within(row('shot.png')).getByText('4')).toBeInTheDocument();
		expect(within(row('docs/plan.md')).queryByText('0')).not.toBeInTheDocument();
	});

	it('marks a file viewed once opened, except the one open now', () => {
		const viewed = new Set(['docs/plan.md', 'shot.png']);
		render(FileList, { ...props, viewed });
		expect(within(row('shot.png')).getByLabelText('viewed')).toBeInTheDocument();
		expect(within(row('docs/plan.md')).queryByLabelText('viewed')).not.toBeInTheDocument();
		expect(within(row('data.bin')).queryByLabelText('viewed')).not.toBeInTheDocument();
	});

	it('marks nothing viewed where viewing means nothing', () => {
		render(FileList, props);
		expect(screen.queryByLabelText('viewed')).not.toBeInTheDocument();
	});
});

describe('FileList as a tree', () => {
	beforeEach(() => prefs.setFileLayout('tree'));

	const titles = () =>
		within(screen.getByRole('navigation', { name: 'Files under review' }))
			.getAllByRole('button')
			.map((b) => b.title);

	it('shows folders first, each file by its name under its folder', () => {
		render(FileList, props);
		expect(titles()).toEqual(['docs', 'docs/plan.md', 'data.bin', 'shot.png']);
		expect(row('docs')).toHaveAttribute('aria-expanded', 'true');
		expect(row('docs/plan.md')).toHaveTextContent(/^\S+\s*plan\.md$/);
	});

	it('folds a folder away and back', async () => {
		render(FileList, props);
		await userEvent.click(row('docs'));
		expect(row('docs')).toHaveAttribute('aria-expanded', 'false');
		expect(titles()).toEqual(['docs', 'data.bin', 'shot.png']);

		await userEvent.click(row('docs'));
		expect(titles()).toEqual(['docs', 'docs/plan.md', 'data.bin', 'shot.png']);
	});

	it('picks a file in a folder, with its comments and marks', async () => {
		const onpick = vi.fn();
		render(FileList, {
			...props,
			current: 'shot.png',
			counts: { 'docs/plan.md': 2 },
			viewed: new Set(['docs/plan.md']),
			onpick
		});
		expect(within(row('docs/plan.md')).getByText('2')).toBeInTheDocument();
		expect(within(row('docs/plan.md')).getByLabelText('viewed')).toBeInTheDocument();
		expect(row('shot.png')).toHaveAttribute('aria-current', 'true');

		await userEvent.click(row('docs/plan.md'));
		expect(onpick).toHaveBeenCalledExactlyOnceWith('docs/plan.md');
	});
});

describe('FileList as a picker', () => {
	const picker = { ...props, variant: 'select' as const };

	it('stays away for a single file', () => {
		render(FileList, { ...picker, files: [plan] });
		expect(screen.queryByRole('combobox')).not.toBeInTheDocument();
	});

	it('says which file of how many is open', () => {
		render(FileList, { ...picker, current: 'shot.png' });
		expect(screen.getByText('2 of 3')).toBeInTheDocument();
	});

	it('shows the open file in the field', () => {
		render(FileList, { ...picker, current: 'shot.png' });
		expect(screen.getByRole('combobox', { name: 'Files under review' })).toHaveTextContent(
			'shot.png'
		);
	});

	it('lists each file with its comments and whether it was viewed', async () => {
		render(FileList, {
			...picker,
			counts: { 'docs/plan.md': 1, 'shot.png': 2 },
			viewed: new Set(['shot.png'])
		});
		await userEvent.click(screen.getByRole('combobox'));
		expect(screen.getAllByRole('option').map((o) => o.textContent?.trim())).toEqual([
			expect.stringContaining(`${I.file} docs/plan.md · 1 comment`),
			expect.stringContaining(`${I.image} shot.png · 2 comments · viewed`),
			expect.stringContaining(`${I.binary} data.bin`)
		]);
	});

	it('picks the file chosen', async () => {
		const onpick = vi.fn();
		render(FileList, { ...picker, onpick });
		await userEvent.click(screen.getByRole('combobox'));
		await userEvent.click(screen.getByRole('option', { name: /shot\.png/ }));
		expect(onpick).toHaveBeenCalledExactlyOnceWith('shot.png');
	});
});
