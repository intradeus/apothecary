import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import Loading from './Loading.svelte';

describe('Loading', () => {
	it('says Loading… as a live status by default', () => {
		render(Loading);
		expect(screen.getByRole('status')).toHaveTextContent('Loading…');
	});

	it('says what it is waiting for', () => {
		render(Loading, { text: 'Connecting…' });
		expect(screen.getByRole('status')).toHaveTextContent('Connecting…');
		expect(screen.queryByText('Loading…')).not.toBeInTheDocument();
	});

	it('colours its dot', () => {
		render(Loading, { hue: 'attention' });
		expect(screen.getByRole('status').querySelector('.live-dot')).toHaveClass('hue-attention');
	});
});
