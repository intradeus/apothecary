import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { text } from '$test/helpers';
import FormPane from './FormPane.svelte';

const props = { id: 'repository', step: 1, title: 'Repository', children: text('the fields') };

describe('FormPane', () => {
	it('is the anchor the step rail scrolls to and watches', () => {
		const { container } = render(FormPane, { ...props, lead: 'where the code lives' });
		expect(container.querySelector('section[id="repository"]')).toBeInTheDocument();
	});

	it('numbers and titles its head, and holds the fields', () => {
		render(FormPane, { ...props, lead: 'where the code lives' });
		expect(screen.getByRole('heading', { name: 'Repository' })).toBeInTheDocument();
		expect(screen.getByText('1')).toBeInTheDocument();
		expect(screen.getByText('where the code lives')).toBeInTheDocument();
		expect(screen.getByText('the fields')).toBeInTheDocument();
	});

	it('takes a lead with markup as a snippet', () => {
		render(FormPane, { ...props, lead: text('a <b>rich</b> lead') });
		expect(screen.getByText('rich')).toBeInTheDocument();
	});
});
