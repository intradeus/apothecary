import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

/** @type {import('@sveltejs/kit').Config} */
export default {
	preprocess: vitePreprocess(),
	kit: {
		// SPA bundle served by apothecaryd (cpui_dir); index.html fallback covers
		// client-side routes like /runs/<id>.
		adapter: adapter({ fallback: 'index.html' })
	}
};
