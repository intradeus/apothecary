import { sveltekit } from '@sveltejs/kit/vite';
import { svelteTesting } from '@testing-library/svelte/vite';
import { defineConfig } from 'vitest/config';

export default defineConfig({
	plugins: [sveltekit(), svelteTesting()],
	server: {
		// Dev mode: same-origin API via proxy to the engine. ws: the run
		// page's terminal is a WebSocket under /api.
		// APOTHECARY_API points it elsewhere (e.g. the local stack on :8081).
		proxy: {
			'/api': { target: process.env.APOTHECARY_API ?? 'http://localhost:8080', ws: true }
		}
	},
	test: {
		environment: 'jsdom',
		// One jsdom per worker, each test file still in its own context: a
		// jsdom per file was a third of the suite's time. Inside a VM context
		// Node's globals (structuredClone, …) build objects of another realm,
		// which Svelte will not make reactive, and `location` cannot be stubbed.
		pool: 'vmThreads',
		include: ['src/**/*.test.ts'],
		setupFiles: ['./src/test/setup.ts'],
		restoreMocks: true,
		// Nothing here is slow: this is for a machine under load, where a timed-out
		// test keeps typing into the next one.
		testTimeout: 30_000,
		coverage: {
			provider: 'v8',
			include: ['src/lib/**/*.{ts,svelte}'],
			exclude: ['**/*.test.ts', 'src/lib/api/generated/**', 'src/lib/i18n/locales/**'],
			reporter: ['text-summary', 'html'],
			// A floor, not a target: raise it when coverage rises, never lower it.
			thresholds: { statements: 98, branches: 92, functions: 98, lines: 99 }
		}
	}
});
