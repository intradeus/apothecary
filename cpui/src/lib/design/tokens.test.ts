import { afterEach, describe, expect, it } from 'vitest';
import { token } from './tokens';

describe('token', () => {
	afterEach(() => {
		document.documentElement.removeAttribute('style');
	});

	it('reads a resolved value from the root, trimmed', () => {
		document.documentElement.style.setProperty('--term-text', '  #e6edf6 ');
		expect(token('--term-text')).toBe('#e6edf6');
	});

	it('reads from the element it is given', () => {
		const el = document.createElement('div');
		document.body.append(el);
		el.style.setProperty('--x', 'near');
		expect(token('--x', el)).toBe('near');
		el.remove();
	});

	it('reads nothing for a token nobody set', () => {
		expect(token('--nothing')).toBe('');
	});
});
