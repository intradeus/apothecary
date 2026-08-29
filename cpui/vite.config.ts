import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [sveltekit()],
	server: {
		// Dev mode: same-origin API via proxy to the engine.
		proxy: { '/api': 'http://localhost:8080' }
	}
});
