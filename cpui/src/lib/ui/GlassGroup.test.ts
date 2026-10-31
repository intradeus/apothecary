import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { html } from '$test/fixtures/designsystem.svelte';
import { text } from '$test/helpers';
import GlassGroup from './GlassGroup.svelte';

describe('GlassGroup', () => {
	it.each([
		['a string', 'Workspace'],
		['a snippet', text('Workspace')]
	])('captions its controls with %s', (_, label) => {
		render(GlassGroup, { label, children: html('<button>Rebuild</button>') });
		expect(screen.getByText('Workspace')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Rebuild' })).toBeInTheDocument();
	});
});
