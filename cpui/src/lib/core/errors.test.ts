import { describe, expect, it } from 'vitest';
import { errorMessage } from './errors';

describe('errorMessage', () => {
	it.each<[unknown, string]>([
		[new Error('the engine said no'), 'the engine said no'],
		[new TypeError('bad type'), 'bad type'],
		['a thrown string', 'a thrown string'],
		[404, '404'],
		[null, 'null'],
		[undefined, 'undefined']
	])('shows %s as its message', (thrown, message) => {
		expect(errorMessage(thrown)).toBe(message);
	});
});
