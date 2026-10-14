import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [sveltekit()],
	server: {
		// Dev mode: same-origin API via proxy to the engine. ws: the run
		// page's terminal is a WebSocket under /api.
		proxy: { '/api': { target: 'http://localhost:8080', ws: true } }
	}
});
