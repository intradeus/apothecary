import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { bindable } from '$test/fixtures/app.svelte';
import type { Annotation } from './annotation';
import AnnotationChip from './AnnotationChip.svelte';

const REF = 'internal/run/run.go:12-13';
const anAnnotation = (over: Partial<Annotation> = {}): Annotation => ({
	id: 'a1',
	ref: REF,
	lang: 'go',
	quote: 'func run() {\n\treturn\n',
	comment: 'Why return here?',
	...over
});

function setup(over: Partial<Annotation> = {}, disabled = false) {
	const props = bindable({ annotation: anAnnotation(over), onremove: vi.fn(), disabled });
	render(AnnotationChip, props);
	return props;
}

const toggle = () => screen.getByRole('button', { name: /lines?$/ });
const comment = () => screen.getByRole('textbox', { name: `Comment on ${REF}` });

describe('AnnotationChip', () => {
	it('shows the reference and how many lines, folded away', () => {
		setup();
		expect(screen.getByRole('group', { name: REF })).toBeInTheDocument();
		expect(toggle()).toHaveTextContent('2 lines');
		expect(toggle()).toHaveAttribute('aria-expanded', 'false');
		expect(toggle()).toHaveAttribute('title', `Show the lines of ${REF}`);
		expect(screen.queryByText(/func run/)).not.toBeInTheDocument();
	});

	it('counts one line in the singular', () => {
		setup({ quote: 'x' });
		expect(toggle()).toHaveTextContent('1 line');
	});

	it('opens to read the lines, and folds them again', async () => {
		setup();
		await userEvent.click(toggle());
		expect(toggle()).toHaveAttribute('aria-expanded', 'true');
		expect(toggle()).toHaveAttribute('title', `Hide the lines of ${REF}`);
		expect(screen.getByText(/func run/)).toHaveTextContent('func run() { return');

		await userEvent.click(toggle());
		expect(screen.queryByText(/func run/)).not.toBeInTheDocument();
	});

	it('edits the comment in place', async () => {
		const props = setup();
		expect(comment()).toHaveValue('Why return here?');

		await userEvent.clear(comment());
		await userEvent.type(comment(), 'Drop it');
		expect(props.annotation.comment).toBe('Drop it');
	});

	it('removes itself', async () => {
		const props = setup();
		await userEvent.click(screen.getByRole('button', { name: `Remove ${REF}` }));
		expect(props.onremove).toHaveBeenCalledOnce();
	});

	it('holds still while its message is on its way', async () => {
		const props = setup({}, true);
		expect(screen.getByRole('button', { name: `Remove ${REF}` })).toBeDisabled();
		expect(comment()).toHaveAttribute('readonly');
		await userEvent.type(comment(), '!');
		expect(props.annotation.comment).toBe('Why return here?');
	});
});
