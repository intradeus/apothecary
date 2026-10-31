import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { portal } from './portal';

describe('portal', () => {
	let home: HTMLElement;
	let node: HTMLElement;

	beforeEach(() => {
		home = document.body.appendChild(document.createElement('section'));
		node = home.appendChild(document.createElement('div'));
	});
	afterEach(() => {
		home.remove();
		node.remove();
	});

	it('moves the element under body by default', () => {
		portal(node);
		expect(node.parentElement).toBe(document.body);
		expect(home).not.toContainElement(node);
	});

	it('leaves it in place when told false', () => {
		portal(node, false);
		expect(node.parentElement).toBe(home);
	});

	it('moves it for an explicit true', () => {
		portal(node, true);
		expect(node.parentElement).toBe(document.body);
	});

	it('takes the element away with it', () => {
		const action = portal(node);
		action?.destroy?.();
		expect(node.isConnected).toBe(false);
	});

	it('does not touch an element it never moved', () => {
		const action = portal(node, false);
		action?.destroy?.();
		expect(node.parentElement).toBe(home);
	});

	it('leaves an element that has since moved elsewhere', () => {
		const action = portal(node);
		home.appendChild(node);
		action?.destroy?.();
		expect(node.parentElement).toBe(home);
	});

	it('copes with an element already gone', () => {
		const action = portal(node);
		node.remove();
		expect(() => action?.destroy?.()).not.toThrow();
	});
});
