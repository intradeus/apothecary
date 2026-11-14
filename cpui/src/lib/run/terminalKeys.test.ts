import { describe, expect, it } from 'vitest';
import { terminalKey, terminalShortcuts, type KeyLike } from './terminalKeys';

type Mods = Partial<Omit<KeyLike, 'key' | 'code'>>;
const ev = (type: string, key: string, mods: Mods = {}, code = `Key${key.toUpperCase()}`) => ({
	type,
	key,
	code,
	ctrlKey: false,
	shiftKey: false,
	altKey: false,
	metaKey: false,
	...mods
});
const ctrl = { ctrlKey: true };
const ctrlShift = { ctrlKey: true, shiftKey: true };

describe('terminalKey', () => {
	describe.each(['keydown', 'keyup'])('on Linux and Windows, %s', (type) => {
		it.each([
			['Ctrl+Shift+C with a selection', ev(type, 'C', ctrlShift), true, 'copy'],
			['Ctrl+Shift+C without one', ev(type, 'C', ctrlShift), false, 'copy'],
			['Ctrl+C with a selection', ev(type, 'c', ctrl), true, 'copy'],
			['Ctrl+C without one (^C)', ev(type, 'c', ctrl), false, 'pass'],
			['Ctrl+V', ev(type, 'v', ctrl), false, 'paste'],
			['Ctrl+Shift+V', ev(type, 'V', ctrlShift), true, 'paste'],
			['Ctrl+Alt+C', ev(type, 'c', { ctrlKey: true, altKey: true }), true, 'pass'],
			['Ctrl+Alt+V', ev(type, 'v', { ctrlKey: true, altKey: true }), false, 'pass'],
			['a plain c', ev(type, 'c'), true, 'pass'],
			['a plain v', ev(type, 'v'), false, 'pass'],
			['Shift+C', ev(type, 'C', { shiftKey: true }), true, 'pass'],
			['Ctrl+D', ev(type, 'd', ctrl), true, 'pass'],
			['Ctrl+Meta+C', ev(type, 'c', { ctrlKey: true, metaKey: true }), true, 'pass']
		])('%s', (_, e, selected, want) => {
			expect(terminalKey(e, selected, false)).toBe(want);
		});
	});

	describe('on another layout', () => {
		it('goes by the key the layout gives a Latin letter', () => {
			// Dvorak: the key labelled C sits where QWERTY has I.
			expect(terminalKey(ev('keydown', 'c', ctrl, 'KeyI'), true, false)).toBe('copy');
			expect(terminalKey(ev('keydown', 'j', ctrl, 'KeyC'), true, false)).toBe('pass');
		});

		it('goes by where the key is when the layout has no Latin letter on it', () => {
			expect(terminalKey(ev('keydown', 'с', ctrl, 'KeyC'), true, false)).toBe('copy');
			expect(terminalKey(ev('keydown', 'м', ctrl, 'KeyV'), false, false)).toBe('paste');
			expect(terminalKey(ev('keydown', 'а', ctrl, 'KeyF'), true, false)).toBe('pass');
		});
	});

	describe.each(['keydown', 'keyup'])('on macOS, %s', (type) => {
		it.each([
			['Ctrl+C, the shell’s ^C', ev(type, 'c', ctrl), true],
			['Ctrl+V, the shell’s ^V', ev(type, 'v', ctrl), false],
			['Ctrl+Shift+C', ev(type, 'C', ctrlShift), true],
			['Cmd+C, the browser’s copy', ev(type, 'c', { metaKey: true }), true],
			['Cmd+V, the browser’s paste', ev(type, 'v', { metaKey: true }), false]
		])('passes %s through', (_, e, selected) => {
			expect(terminalKey(e, selected, true)).toBe('pass');
		});
	});
});

describe('terminalShortcuts', () => {
	it('names Ctrl+Shift off a Mac', () => {
		expect(terminalShortcuts(false)).toEqual({ copy: 'Ctrl+Shift+C', paste: 'Ctrl+Shift+V' });
	});

	it('names Cmd on a Mac', () => {
		expect(terminalShortcuts(true)).toEqual({ copy: '⌘C', paste: '⌘V' });
	});
});
