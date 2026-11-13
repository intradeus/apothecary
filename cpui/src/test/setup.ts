// Runs before every test file. jsdom has no layout and none of the observers,
// so what the app calls gets a quiet stand-in here; a test that cares about
// geometry stubs it itself. Nothing reaches the network: `$lib/api` is mocked
// per test, and a fetch that slips through fails loudly.
import '@testing-library/jest-dom/vitest';
import { configure } from '@testing-library/svelte/pure';
import { vi } from 'vitest';

// One budget for every async wait. Testing Library's waitFor and findBy* and
// vitest's vi.waitFor each give up after 1 s by default, which a loaded
// machine misses: under `task ci`, every suite at once, tests ran about 8×
// slower (983 took over a second, against 120 alone) and two waits that hold
// in milliseconds on an idle machine timed out. A wait still ends the moment
// its condition holds, so only a real failure takes longer to report, well
// inside testTimeout (vite.config.ts). A wait that names its own timeout
// keeps it.
export const ASYNC_TIMEOUT = 10_000;
configure({ asyncUtilTimeout: ASYNC_TIMEOUT });
const viWaitFor = vi.waitFor.bind(vi);
vi.waitFor = ((callback, options) =>
	viWaitFor(
		callback,
		typeof options === 'number' ? options : { timeout: ASYNC_TIMEOUT, ...options }
	)) as typeof vi.waitFor;

class NoObserver {
	observe() {}
	unobserve() {}
	disconnect() {}
	takeRecords() {
		return [];
	}
}
vi.stubGlobal('ResizeObserver', NoObserver);
vi.stubGlobal('IntersectionObserver', NoObserver);

vi.stubGlobal('matchMedia', (query: string) => ({
	matches: false,
	media: query,
	addEventListener() {},
	removeEventListener() {}
}));

vi.stubGlobal('fetch', (url: string) =>
	Promise.reject(new Error(`unmocked fetch in a test: ${url}`))
);

Element.prototype.scrollIntoView = () => {};
Element.prototype.scrollTo = () => {};
Element.prototype.setPointerCapture = () => {};
Element.prototype.releasePointerCapture = () => {};
Element.prototype.hasPointerCapture = () => false;
Element.prototype.getAnimations = () => [];

// Svelte transitions wait on onfinish, so an element can leave the DOM.
Element.prototype.animate = () => {
	const animation = {
		onfinish: null as (() => void) | null,
		oncancel: null,
		currentTime: 0,
		finished: Promise.resolve(),
		cancel() {},
		finish() {}
	};
	setTimeout(() => animation.onfinish?.(), 0);
	return animation as unknown as Animation;
};

vi.mock('$app/navigation', () => ({
	goto: vi.fn(),
	invalidate: vi.fn(),
	invalidateAll: vi.fn(),
	beforeNavigate: vi.fn(),
	afterNavigate: vi.fn(),
	onNavigate: vi.fn(),
	pushState: vi.fn(),
	replaceState: vi.fn()
}));
vi.mock('$app/state', () => ({
	page: { url: new URL('http://localhost/'), params: {}, route: { id: '/' } },
	navigating: null,
	updated: { current: false }
}));
