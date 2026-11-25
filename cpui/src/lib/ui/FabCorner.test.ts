import { render, screen } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import { describe, expect, it } from 'vitest';
import FabCorner from './FabCorner.svelte';

describe('FabCorner', () => {
	it('holds the buttons it is given, in their order', () => {
		render(FabCorner, {
			children: createRawSnippet(() => ({
				render: () =>
					'<span><button class="fab">Layout</button><button class="fab">Ask</button></span>'
			}))
		});
		const [first, second] = screen.getAllByRole('button');
		expect([first.textContent, second.textContent]).toEqual(['Layout', 'Ask']);
		expect(first.closest('.corner')).not.toBeNull();
	});
});
