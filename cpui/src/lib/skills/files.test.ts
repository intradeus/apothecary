import { describe, expect, it } from 'vitest';
import {
	decodeText,
	encodeText,
	fillFiles,
	isText,
	languageFor,
	SKILL_NAME,
	textFiles,
	type EditFile
} from './files';

describe('isText', () => {
	it('takes UTF-8, accents and all', () => {
		expect(isText(encodeText('# Skill – café ☕'))).toBe(true);
	});

	it('takes an empty file', () => {
		expect(isText(new Uint8Array())).toBe(true);
	});

	it('refuses a NUL near the start', () => {
		expect(isText(new Uint8Array([0x68, 0x00, 0x69]))).toBe(false);
	});

	it('looks for NUL only in the first 8 KiB', () => {
		const late = new Uint8Array(8193).fill(0x61);
		late[8192] = 0;
		expect(isText(late)).toBe(true);

		const edge = new Uint8Array(8192).fill(0x61);
		edge[8191] = 0;
		expect(isText(edge)).toBe(false);
	});

	it('refuses bytes that are not UTF-8', () => {
		expect(isText(new Uint8Array([0x89, 0x50, 0x4e, 0x47]))).toBe(false);
	});
});

describe('decodeText and encodeText', () => {
	it('round-trips text', () => {
		const text = 'line one\nline two – ñ';
		expect(decodeText(encodeText(text))).toBe(text);
	});

	it('throws on bytes that are not UTF-8', () => {
		expect(() => decodeText(new Uint8Array([0xff]))).toThrow();
	});
});

describe('languageFor', () => {
	it.each([
		['SKILL.md', 'markdown'],
		['docs/notes.MARKDOWN', 'markdown'],
		['run.sh', 'plain'],
		['md', 'plain'],
		['notes.md.bak', 'plain']
	])('reads %s as %s', (path, language) => {
		expect(languageFor(path)).toBe(language);
	});
});

describe('SKILL_NAME', () => {
	it.each(['review', 'code-review', 'a1-b2-c3'])('accepts %s', (name) => {
		expect(SKILL_NAME.test(name)).toBe(true);
	});

	it.each(['', 'Review', '-review', 'review-', 'code--review', 'code_review', 'code review'])(
		'refuses %j',
		(name) => {
			expect(SKILL_NAME.test(name)).toBe(false);
		}
	);
});

describe('textFiles and fillFiles', () => {
	const bin = new Uint8Array([0, 1, 2]);
	const files: EditFile[] = [
		{ path: 'SKILL.md', bytes: new Uint8Array(), text: 'old' },
		{ path: 'run.sh', bytes: new Uint8Array(), mode: 0o755, text: 'echo' },
		{ path: 'logo.png', bytes: bin, text: null },
		{ path: 'blob.bin', bytes: bin, text: null }
	];

	it('reads the text files only', () => {
		expect(textFiles(files)).toEqual([
			{ path: 'SKILL.md', content: 'old' },
			{ path: 'run.sh', content: 'echo' }
		]);
	});

	it('makes the fill the text files, keeps modes and the files that are not text', () => {
		const out = fillFiles(files, [
			{ path: 'SKILL.md', content: 'new' },
			{ path: 'run.sh', content: 'echo hi' },
			{ path: 'blob.bin', content: 'now text' }
		]);
		expect(out.map((f) => [f.path, f.text, f.mode])).toEqual([
			['SKILL.md', 'new', undefined],
			['run.sh', 'echo hi', 0o755],
			['blob.bin', 'now text', undefined],
			['logo.png', null, undefined]
		]);
		expect(out[3].bytes).toBe(bin);
	});

	it('drops a text file the fill leaves out', () => {
		expect(fillFiles(files, [{ path: 'SKILL.md', content: 'x' }]).map((f) => f.path)).toEqual([
			'SKILL.md',
			'logo.png',
			'blob.bin'
		]);
	});
});
