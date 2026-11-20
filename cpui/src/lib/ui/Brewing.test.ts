import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import Brewing from './Brewing.svelte';

describe('Brewing', () => {
	it('is a status that says it is brewing', () => {
		render(Brewing);
		expect(screen.getByRole('status')).toHaveTextContent('Brewing…');
	});

	it('says what is brewing when told', () => {
		render(Brewing, { label: 'Filling the ticket…', size: 'lg' });
		expect(screen.getByRole('status')).toHaveTextContent('Filling the ticket…');
	});
});
