import { createEvent, fireEvent, render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { flushSync } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { UploadLimits } from '$lib/domain';
import { bound, settled } from '$test/fixtures/run-components';
import { aFile } from '$test/fixtures/run.svelte';
import AttachFiles from './AttachFiles.svelte';

// The picker reads the limits once a session, into module state. Every test
// sees this one object, so each states what it needs by writing to it.
const limits = vi.hoisted(() => ({
	maxBytes: 1024,
	maxFiles: 3,
	mimeAllow: [] as string[]
}));

vi.mock('$lib/api', () => ({
	api: { files: { limits: vi.fn(async () => limits) } }
}));

const DEFAULTS: UploadLimits = {
	maxBytes: 1024,
	maxFiles: 3,
	mimeAllow: ['image/*', 'application/pdf', 'text/markdown', 'text/plain']
};

async function setup(initial: File[] = [], over = {}) {
	const props = bound({ files: initial, ...over });
	const view = render(AttachFiles, { props });
	await settled();
	const input = view.container.querySelector('input[type="file"]') as HTMLInputElement;
	return { props, input, ...view };
}

const zone = () => screen.getByRole('button', { name: /drop files or browse/i });
const drop = (...files: File[]) => fireEvent.drop(zone(), { dataTransfer: { files } });
const dropEvent = (...files: File[]) => createEvent.drop(zone(), { dataTransfer: { files } });
const pick = (input: HTMLInputElement, ...files: File[]) =>
	userEvent.upload(input, files, { applyAccept: false });

beforeEach(() => {
	Object.assign(limits, DEFAULTS, { mimeAllow: [...DEFAULTS.mimeAllow] });
	URL.createObjectURL = vi.fn(() => 'blob:preview'); // jsdom has none
});

describe('AttachFiles', () => {
	describe('the drop zone', () => {
		it('says what it takes', async () => {
			await setup();
			expect(zone()).toHaveTextContent('· max 1 KB · images, pdf, md, text');
		});

		it('leaves a compact one to the point', async () => {
			await setup([], { compact: true });
			expect(zone()).not.toHaveTextContent('max');
		});

		it('takes its label from the caller', async () => {
			await setup([], { label: 'Attach to your answer' });
			expect(screen.getByRole('button', { name: /attach to your answer/i })).toBeInTheDocument();
		});

		it('opens the file dialog', async () => {
			const { input } = await setup();
			const click = vi.spyOn(input, 'click');
			await userEvent.click(zone());
			expect(click).toHaveBeenCalledOnce();
		});

		it('lets the browser drop by cancelling the default', async () => {
			await setup();
			const e = dropEvent(aFile());
			fireEvent(zone(), e);
			expect(e.defaultPrevented).toBe(true);
		});

		it('lights up under a dragged file, and goes out when it leaves', async () => {
			await setup();
			await fireEvent.dragOver(zone());
			expect(zone()).toHaveClass('over');
			await fireEvent.dragLeave(zone());
			expect(zone()).not.toHaveClass('over');
		});

		it('goes out on a drop', async () => {
			await setup();
			await fireEvent.dragOver(zone());
			await drop(aFile());
			expect(zone()).not.toHaveClass('over');
		});
	});

	describe('taking files', () => {
		it('adds what is picked', async () => {
			const { input, props } = await setup();
			const file = aFile('a.txt');
			await pick(input, file);
			expect(props.files).toEqual([file]);
			expect(screen.getByText('a.txt')).toBeInTheDocument();
		});

		it('adds what is dropped', async () => {
			const { props } = await setup();
			const file = aFile('a.txt');
			await drop(file);
			expect(props.files).toEqual([file]);
		});

		it('adds to what is there', async () => {
			const first = aFile('a.txt');
			const { input, props } = await setup([first]);
			const second = aFile('b.txt');
			await pick(input, second);
			expect(props.files).toEqual([first, second]);
		});

		it('clears the input so the same file can be picked again', async () => {
			const { input } = await setup();
			await pick(input, aFile());
			expect(input.value).toBe('');
		});

		it('removes a chip', async () => {
			const a = aFile('a.txt');
			const b = aFile('b.txt');
			const { props } = await setup([a, b]);
			await userEvent.click(screen.getByRole('button', { name: 'Remove a.txt' }));
			expect(props.files).toEqual([b]);
			expect(screen.queryByText('a.txt')).not.toBeInTheDocument();
		});

		it('thumbnails an image from the picked file', async () => {
			const image = aFile('a.png', 'image/png');
			const { container } = await setup([image]);
			expect(URL.createObjectURL).toHaveBeenCalledWith(image);
			expect(container.querySelector('img')).toHaveAttribute('src', 'blob:preview');
		});

		it('gives a document no thumbnail', async () => {
			const { container } = await setup([aFile('a.txt')]);
			expect(container.querySelector('img')).toBeNull();
		});
	});

	describe('refusing a file', () => {
		it('refuses one over the size limit, with the limit', async () => {
			const { input, props } = await setup();
			await pick(input, aFile('big.txt', 'text/plain', 2048));
			expect(props.files).toEqual([]);
			expect(screen.getByText('big.txt')).toBeInTheDocument();
			expect(screen.getByText('over the 1 KB limit')).toBeInTheDocument();
		});

		it('takes a file of exactly the limit', async () => {
			const { input, props } = await setup();
			await pick(input, aFile('fits.txt', 'text/plain', 1024));
			expect(props.files).toHaveLength(1);
		});

		it('refuses a type off the allowlist, naming it', async () => {
			const { input, props } = await setup();
			await pick(input, aFile('x.zip', 'application/zip'));
			expect(props.files).toEqual([]);
			expect(screen.getByText('application/zip is not allowed')).toBeInTheDocument();
		});

		it.each([
			['a wildcard', 'image/webp'],
			['an exact match', 'application/pdf'],
			['a parameter on the type', 'text/plain; charset=utf-8']
		])('takes %s', async (_, type) => {
			const { input, props } = await setup();
			await pick(input, aFile('ok', type));
			expect(props.files).toHaveLength(1);
		});

		it('lets an unknown type through for the server to sniff', async () => {
			const { input, props } = await setup();
			await pick(input, aFile('mystery', ''));
			expect(props.files).toHaveLength(1);
		});

		it('takes up to the file limit, counting what is there, and refuses the rest', async () => {
			limits.maxFiles = 2;
			const { input, props } = await setup([aFile('a.txt')]);
			const second = aFile('b.txt');
			await pick(input, second, aFile('c.txt'));
			expect(props.files.map((f) => f.name)).toEqual(['a.txt', 'b.txt']);
			expect(screen.getByText('at most 2 files')).toBeInTheDocument();
		});

		it('says file, singular, for a limit of one', async () => {
			limits.maxFiles = 1;
			const { input } = await setup([aFile('a.txt')]);
			await pick(input, aFile('b.txt'));
			expect(screen.getByText('at most 1 file')).toBeInTheDocument();
		});

		it('keeps the good files of a mixed pick', async () => {
			const { input, props } = await setup();
			await pick(input, aFile('good.txt'), aFile('bad.zip', 'application/zip'));
			expect(props.files.map((f) => f.name)).toEqual(['good.txt']);
			expect(screen.getByText('bad.zip')).toBeInTheDocument();
		});

		it('offers no way to remove a refused file', async () => {
			const { input } = await setup();
			await pick(input, aFile('bad.zip', 'application/zip'));
			expect(screen.queryByRole('button', { name: /remove/i })).not.toBeInTheDocument();
		});

		it('forgets the refusals on a drop that brings nothing', async () => {
			const { input } = await setup();
			await pick(input, aFile('bad.zip', 'application/zip'));
			await fireEvent.drop(zone());
			expect(screen.queryByText('bad.zip')).not.toBeInTheDocument();
		});

		it('forgets the refusals on the next pick', async () => {
			const { input } = await setup();
			await pick(input, aFile('bad.zip', 'application/zip'));
			await pick(input, aFile('good.txt'));
			expect(screen.queryByText('bad.zip')).not.toBeInTheDocument();
		});

		// Two same-named files dropped from different folders.
		it('lists two refused files with the same name', async () => {
			await setup();
			const twins = [aFile('dup.zip', 'application/zip'), aFile('dup.zip', 'application/zip')];
			zone().dispatchEvent(dropEvent(...twins));
			flushSync(); // the throw lands here, in the test, not as an unhandled error
			expect(screen.getAllByText('dup.zip')).toHaveLength(2);
		});
	});

	it('reads the limits once, for every picker', async () => {
		await setup();
		await setup();
		expect(vi.mocked(api.files.limits).mock.calls.length).toBeLessThanOrEqual(1);
	});

	// The first test in the file to mount would see this, so a module reload
	// gives this test a picker whose limits have not been read.
	it('takes anything while the limits are unknown, leaving it to the server', async () => {
		vi.resetModules();
		const fresh = await import('$lib/api');
		vi.mocked(fresh.api.files.limits).mockResolvedValue(null as never);
		const testing = await import('@testing-library/svelte');
		const { default: Picker } = await import('./AttachFiles.svelte');
		const { bound: freshBound } = await import('$test/fixtures/run-components');

		const props = freshBound({ files: [] as File[] });
		const view = testing.render(Picker, { props });
		await settled();
		const input = view.container.querySelector('input[type="file"]') as HTMLInputElement;

		await pick(input, aFile('huge.zip', 'application/zip', 4096));

		expect(props.files).toHaveLength(1);
		expect(testing.screen.getByRole('button', { name: /drop files/i })).not.toHaveTextContent(
			'max'
		);
		view.unmount();
	});
});
