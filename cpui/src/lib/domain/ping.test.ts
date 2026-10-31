import { describe, expect, it } from 'vitest';
import { REPLY_DIDS, REVIEW_KINDS } from './ping';

describe('review vocabulary', () => {
	it('names the kinds of a note the review types accept', () => {
		expect(REVIEW_KINDS).toEqual(['blocking', 'suggestion', 'question', 'nit']);
	});

	it('names what a reply may have done', () => {
		expect(REPLY_DIDS).toEqual(['fixed', 'declined', 'answered']);
	});
});
