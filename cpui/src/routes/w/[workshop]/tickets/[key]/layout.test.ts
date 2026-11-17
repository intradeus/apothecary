import { render } from '@testing-library/svelte';
import { createRawSnippet, flushSync } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import { page } from '$app/state';
import Layout from './+layout.svelte';

// The route's params as SvelteKit's page state holds them: reactive, so a
// navigation that changes only some of them reaches the layout. vi.mock is
// hoisted above the imports, so its factory loads the rune fixture itself.
vi.mock('$app/state', async () => {
	const { bindable } = await import('$test/fixtures/app.svelte');
	return { page: bindable({ params: { workshop: 'p-a', key: 'PROJ-7' } }) };
});

describe('the ticket route', () => {
	it('mounts a fresh page for each workshop and each key', () => {
		let mounts = 0;
		const children = createRawSnippet(() => ({
			render: () => '<p>ticket</p>',
			setup: () => {
				mounts++;
			}
		}));
		render(Layout, { children });
		expect(mounts).toBe(1);

		// The same issue through another workshop on its source.
		page.params.workshop = 'p-b';
		flushSync();
		expect(mounts).toBe(2);

		page.params.key = 'PROJ-8';
		flushSync();
		expect(mounts).toBe(3);
	});
});
