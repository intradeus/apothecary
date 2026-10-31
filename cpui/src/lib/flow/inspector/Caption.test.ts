import { render, screen } from '@testing-library/svelte';
import { expect, it } from 'vitest';
import Caption from './Caption.svelte';

it('shows its text', () => {
	render(Caption, { text: 'Routes' });
	expect(screen.getByText('Routes')).toBeInTheDocument();
});
