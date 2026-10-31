import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { fill } from '$test/fixtures/flow.svelte';
import SlugField, { slugOk } from './SlugField.svelte';

const props = { id: 'n', value: '', help: 'The flow’s id.' };
const field = () => screen.getByLabelText('Name');

describe('slugOk', () => {
	it.each([
		['v2-fast', true],
		['a_b', true],
		['  padded ', true],
		['Upper', false],
		['2fast', false],
		['has space', false],
		['', false]
	])('%j → %s', (s, ok) => {
		expect(slugOk(s)).toBe(ok);
	});
});

describe('SlugField', () => {
	it('says what the name is while it is fine', () => {
		render(SlugField, props);
		expect(screen.getByText('The flow’s id.')).toBeInTheDocument();
		expect(field()).not.toHaveClass('invalid');
	});

	it('uses the label it is given', () => {
		render(SlugField, { ...props, label: 'Copy as' });
		expect(screen.getByLabelText('Copy as')).toBeInTheDocument();
	});

	it('checks the slug as it is typed', async () => {
		render(SlugField, props);
		await fill(field(), 'Not Ok');
		expect(screen.getByText(/A lowercase slug/)).toBeInTheDocument();
		expect(screen.queryByText('The flow’s id.')).not.toBeInTheDocument();
		expect(field()).toHaveClass('invalid');
	});

	it('shows the caller’s conflict in place of the help', () => {
		render(SlugField, { ...props, value: 'builder', conflict: 'Taken.' });
		expect(screen.getByText('Taken.')).toBeInTheDocument();
		expect(field()).toHaveClass('invalid');
	});

	it('puts a bad slug before a conflict', () => {
		render(SlugField, { ...props, value: 'Bad', conflict: 'Taken.' });
		expect(screen.getByText(/A lowercase slug/)).toBeInTheDocument();
		expect(screen.queryByText('Taken.')).not.toBeInTheDocument();
	});

	it('does not call an empty field wrong', () => {
		render(SlugField, props);
		expect(screen.queryByText(/A lowercase slug/)).not.toBeInTheDocument();
	});
});
