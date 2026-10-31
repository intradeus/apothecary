import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { text } from '$test/helpers';
import AddCard from './AddCard.svelte';

describe('AddCard', () => {
	it('titles the form it holds', () => {
		render(AddCard, { title: 'New integration', children: text('the form') });
		expect(screen.getByText('New integration')).toBeInTheDocument();
		expect(screen.getByText('the form')).toBeInTheDocument();
	});

	it('shows a kind chip and head controls when given them', () => {
		render(AddCard, {
			title: 'New integration',
			kind: 'GitLab',
			head: text('a kind picker'),
			children: text('the form')
		});
		expect(screen.getByText('GitLab')).toBeInTheDocument();
		expect(screen.getByText('a kind picker')).toBeInTheDocument();
	});
});
