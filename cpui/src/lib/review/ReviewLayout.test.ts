import { fireEvent, render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { text } from '$test/helpers';
import ReviewLayout from './ReviewLayout.svelte';

const panes = { nav: text('the files'), main: text('the file'), side: text('the comments') };

describe('ReviewLayout', () => {
	it('frames the files, the open file and the comments', () => {
		render(ReviewLayout, panes);
		expect(screen.getByText('the files')).toBeInTheDocument();
		expect(screen.getByText('the file').closest('section')).toBeInTheDocument();
		expect(screen.getByText('the comments').closest('aside')).toBeInTheDocument();
	});

	it.each([
		['j', 1],
		['k', -1],
		[']', 1],
		['[', -1]
	])('%s moves %i along the files', async (key, d) => {
		const onstep = vi.fn();
		render(ReviewLayout, { ...panes, onstep });
		await fireEvent.keyDown(document.body, { key });
		expect(onstep).toHaveBeenCalledExactlyOnceWith(d);
	});

	it('takes the key away from the page when it moves', async () => {
		render(ReviewLayout, { ...panes, onstep: vi.fn() });
		// fireEvent answers false when the default was prevented.
		expect(await fireEvent.keyDown(document.body, { key: 'j' })).toBe(false);
		expect(await fireEvent.keyDown(document.body, { key: 'x' })).toBe(true);
	});

	it.each([
		['another key', { key: 'x' }],
		['j with ctrl', { key: 'j', ctrlKey: true }],
		['j with cmd', { key: 'j', metaKey: true }],
		['j with alt', { key: 'j', altKey: true }]
	])('leaves %s alone', async (_, init) => {
		const onstep = vi.fn();
		render(ReviewLayout, { ...panes, onstep });
		await fireEvent.keyDown(document.body, init);
		expect(onstep).not.toHaveBeenCalled();
	});

	it('leaves a key something else already took', async () => {
		const onstep = vi.fn();
		const main = text('<button>inner</button>');
		render(ReviewLayout, { ...panes, main, onstep });
		const inner = screen.getByRole('button', { name: 'inner' });
		inner.addEventListener('keydown', (e) => e.preventDefault());
		await fireEvent.keyDown(inner, { key: 'j' });
		expect(onstep).not.toHaveBeenCalled();
	});

	it.each([
		['an input', '<input aria-label="field" />', 'textbox'],
		['a textarea', '<textarea aria-label="field"></textarea>', 'textbox'],
		['a select', '<select aria-label="field"></select>', 'combobox'],
		[
			'an editable region',
			'<div role="textbox" aria-label="field" contenteditable="true"></div>',
			'textbox'
		]
	])('leaves typing in %s alone', async (_, html, role) => {
		const onstep = vi.fn();
		render(ReviewLayout, { ...panes, side: text(html), onstep });
		await fireEvent.keyDown(screen.getByRole(role, { name: 'field' }), { key: 'j' });
		expect(onstep).not.toHaveBeenCalled();
	});

	it('still moves from a region marked not editable', async () => {
		const onstep = vi.fn();
		const side = text('<div role="note" aria-label="field" contenteditable="false"></div>');
		render(ReviewLayout, { ...panes, side, onstep });
		await fireEvent.keyDown(screen.getByRole('note', { name: 'field' }), { key: 'j' });
		expect(onstep).toHaveBeenCalledOnce();
	});

	it('keeps j and k for the page without onstep', async () => {
		render(ReviewLayout, panes);
		expect(await fireEvent.keyDown(document.body, { key: 'j' })).toBe(true);
	});

	it('reacts to a real keypress in the window', async () => {
		const onstep = vi.fn();
		render(ReviewLayout, { ...panes, onstep });
		await userEvent.keyboard('k');
		expect(onstep).toHaveBeenCalledExactlyOnceWith(-1);
	});
});
