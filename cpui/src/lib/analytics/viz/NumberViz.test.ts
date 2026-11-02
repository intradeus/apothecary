import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { aResult } from '$test/fixtures/analytics';
import NumberViz from './NumberViz.svelte';

describe('NumberViz', () => {
	it('shows the first value of the first row, formatted, with its n', () => {
		render(NumberViz, { result: aResult() });
		expect(screen.getByText('$0.250')).toBeInTheDocument();
		expect(screen.getByText('n = 12')).toBeInTheDocument();
	});

	it('shows the count when the query has no measures', () => {
		render(NumberViz, {
			result: aResult({
				dims: [],
				measures: [],
				rows: [{ dims: [], values: [], n: 42, small: false }]
			})
		});
		expect(screen.getByText('42')).toBeInTheDocument();
		expect(screen.getByText('n = 42')).toBeInTheDocument();
	});

	it('shows a dash when nothing matched', () => {
		render(NumberViz, { result: aResult({ rows: [] }) });
		expect(screen.getByText('—')).toBeInTheDocument();
		expect(screen.getByText('n = 0')).toBeInTheDocument();
	});
});
