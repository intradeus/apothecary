import { beforeEach, describe, expect, it, vi } from 'vitest';
import { aBlock, aComment, aReviewComment, select } from '$test/fixtures/review';
import {
	byPlace,
	clearDraft,
	commentAsShown,
	commentToSend,
	docBlocks,
	fileKind,
	isImagePath,
	isMarkdown,
	isWholeFile,
	listing,
	loadDraft,
	locate,
	looksBinary,
	perFile,
	rangeAt,
	saveDraft,
	textOffset
} from './reviewDoc';

const KEY = 'doc-review-p1';

describe('commentToSend', () => {
	it('sends a comment on a selection with where it sits, trimmed, without its anchor', () => {
		const sent = commentToSend(
			aComment({
				quote: 'double charge',
				heading: 'Risks',
				startLine: 7,
				endLine: 9,
				text: '  Say how.\n',
				anchor: { startBlock: 3, startOffset: 1, endBlock: 3, endOffset: 9 }
			}),
			['att-1']
		);
		expect(sent).toEqual({
			path: 'plan.md',
			startLine: 7,
			endLine: 9,
			heading: 'Risks',
			quote: 'double charge',
			body: 'Say how.',
			attachmentIds: ['att-1']
		});
	});

	it('sends a whole-file comment without lines or a quote', () => {
		const sent = commentToSend(aComment({ startLine: 0, endLine: 0, quote: 'left over' }));
		expect(sent.startLine).toBeUndefined();
		expect(sent.endLine).toBeUndefined();
		expect(sent.quote).toBeUndefined();
	});

	it('leaves an empty heading and quote out, and attaches nothing by default', () => {
		const sent = commentToSend(aComment({ startLine: 2, endLine: 2 }));
		expect(sent.heading).toBeUndefined();
		expect(sent.quote).toBeUndefined();
		expect(sent.attachmentIds).toEqual([]);
	});
});

describe('commentAsShown', () => {
	it('holds a submitted comment the way the page shows one', () => {
		const shown = commentAsShown(
			aReviewComment({
				startLine: 4,
				endLine: 6,
				heading: 'Risks',
				quote: 'retry',
				body: 'Why?',
				attachmentIds: ['a']
			}),
			2
		);
		expect(shown).toEqual({
			id: 'r2',
			path: 'plan.md',
			quote: 'retry',
			heading: 'Risks',
			startLine: 4,
			endLine: 6,
			text: 'Why?',
			attachmentIds: ['a']
		});
	});

	it('reads a comment without lines as being on the whole file', () => {
		const shown = commentAsShown(aReviewComment(), 0);
		expect(isWholeFile(shown)).toBe(true);
		expect(shown).toMatchObject({ quote: '', heading: '', startLine: 0, endLine: 0 });
	});

	it('ends a comment that names only its first line on that line', () => {
		expect(commentAsShown(aReviewComment({ startLine: 8 }), 0).endLine).toBe(8);
	});
});

describe('isImagePath', () => {
	it.each([
		['shot.png', true],
		['docs/SHOT.PNG', true],
		['logo.svg', true],
		['photo.jpeg', true],
		['icon.ico', true],
		['png', false],
		['notes.png.txt', false],
		['main.ts', false]
	])('%s → %s', (path, want) => {
		expect(isImagePath(path)).toBe(want);
	});
});

describe('fileKind', () => {
	it.each([
		['an image by its name, whatever its type', 'a.png', 'text/plain', 'image'],
		['an image by its type, whatever its name', 'a.dat', 'image/heic', 'image'],
		['text with no type at all', 'a.txt', undefined, 'text'],
		['text under an unknown type', 'a.lock', 'application/octet-stream', 'text'],
		['TypeScript, which the extension calls MPEG video', 'a.ts', 'video/mp2t', 'text'],
		['video', 'a.mp4', 'video/mp4', 'other'],
		['audio', 'a.mp3', 'audio/mpeg', 'other'],
		['a font', 'a.woff2', 'font/woff2', 'other'],
		['a pdf', 'a.pdf', 'application/pdf', 'other'],
		['an archive', 'a.zip', 'application/zip', 'other'],
		['a wasm module', 'a.wasm', 'application/wasm', 'other'],
		['an office document', 'a.docx', 'application/vnd.ms-word', 'other'],
		['json', 'a.json', 'application/json', 'text']
	])('reads %s', (_, path, mime, want) => {
		expect(fileKind({ path, mime })).toBe(want);
	});
});

describe('looksBinary', () => {
	const withNulAt = (i: number) => {
		const bytes = new Uint8Array(9000).fill(65);
		bytes[i] = 0;
		return bytes;
	};

	it.each([
		['plain bytes', new Uint8Array([65, 66, 67]), false],
		['nothing at all', new Uint8Array(), false],
		['a NUL at the head', new Uint8Array([0, 65]), true],
		['a NUL on the last byte sniffed', withNulAt(8191), true],
		['a NUL past the head', withNulAt(8192), false]
	])('%s → %s', (_, bytes, want) => {
		expect(looksBinary(bytes)).toBe(want);
	});
});

describe('perFile', () => {
	it('counts the comments on each path', () => {
		const counts = perFile([{ path: 'a' }, { path: 'b' }, { path: 'a' }]);
		expect(counts).toEqual({ a: 2, b: 1 });
	});

	it('is empty for no comments', () => {
		expect(perFile([])).toEqual({});
	});
});

describe('byPlace', () => {
	const order = ['b.md', 'a.md'];
	const at = (path: string, startLine: number, over = {}) =>
		aComment({ id: `${path}:${startLine}`, path, startLine, endLine: startLine, ...over });
	const sorted = (...comments: ReturnType<typeof at>[]) =>
		comments.sort(byPlace(order)).map((c) => c.text);

	it('orders by file as the files are shown, not by name', () => {
		const c = [at('a.md', 1, { text: 'second' }), at('b.md', 9, { text: 'first' })];
		expect(sorted(...c)).toEqual(['first', 'second']);
	});

	it('puts a file no longer shown last', () => {
		const c = [at('gone.md', 1, { text: 'last' }), at('a.md', 50, { text: 'first' })];
		expect(sorted(...c)).toEqual(['first', 'last']);
	});

	it('puts whole-file comments before any line of that file', () => {
		const c = [at('a.md', 3, { text: 'line' }), at('a.md', 0, { text: 'whole' })];
		expect(sorted(...c)).toEqual(['whole', 'line']);
	});

	it('orders by line, then where the selection starts, then where it ends', () => {
		const anchor = (startOffset: number) => ({
			anchor: { startBlock: 0, startOffset, endBlock: 0, endOffset: 40 }
		});
		const c = [
			at('a.md', 5, { text: 'later line' }),
			at('a.md', 4, { text: 'further along', ...anchor(9) }),
			at('a.md', 4, { text: 'long', ...anchor(2), endLine: 8 }),
			at('a.md', 4, { text: 'short', ...anchor(2) })
		];
		expect(sorted(...c)).toEqual(['short', 'long', 'further along', 'later line']);
	});
});

describe('isMarkdown', () => {
	it.each([
		['plan.md', true],
		['PLAN.MD', true],
		['docs/plan.markdown', true],
		['post.mdx', true],
		['NOTES', true],
		['some.dir/NOTES', true],
		['.gitignore', false],
		['config.yaml', false],
		['main.ts', false],
		['docs.md/main.ts', false]
	])('%s → %s', (path, want) => {
		expect(isMarkdown(path)).toBe(want);
	});
});

describe('listing', () => {
	it.each([
		['fences a file in its language', 'a: 1\n', 'cfg.YAML', '```yaml\na: 1\n```'],
		['names no language without an extension', 'all:\n', 'dir.v1/Makefile', '```\nall:\n```'],
		['drops one trailing newline, no more', 'x\n\n', 'a.txt', '```txt\nx\n\n```'],
		['outgrows a fence in the text', '```\ncode\n```', 'a.txt', '````txt\n```\ncode\n```\n````'],
		['outgrows the longest fence', '`````\n```', 'a.txt', '``````txt\n`````\n```\n``````'],
		['ignores backticks too short to close it', 'a `` b', 'a.txt', '```txt\na `` b\n```']
	])('%s', (_, src, path, want) => {
		expect(listing(src, path)).toBe(want);
	});
});

describe('docBlocks', () => {
	const shape = (src: string, path = 'plan.md') =>
		docBlocks(src, path).map((b) => [b.heading, b.startLine, b.endLine]);

	it.each([
		[
			'carries the nearest heading above each block',
			'# A\n\nfirst\n\n## B\n\nsecond\n',
			[
				['A', 1, 1],
				['A', 3, 3],
				['B', 5, 5],
				['B', 7, 7]
			]
		],
		[
			'leaves text above any heading without one',
			'intro\n\n# A\n',
			[
				['', 1, 1],
				['A', 3, 3]
			]
		],
		[
			'counts every blank line',
			'# A\n\n\n\nbody\n',
			[
				['A', 1, 1],
				['A', 5, 5]
			]
		],
		[
			'splits text directly under a heading',
			'# A\nbody\n',
			[
				['A', 1, 1],
				['A', 2, 2]
			]
		],
		['ends a multi-line block closed by a newline', 'one\ntwo\n', [['', 1, 2]]],
		['nothing in an empty file', '', []],
		['nothing in a blank file', '\n\n', []]
	])('%s', (_, src, want) => {
		expect(shape(src)).toEqual(want);
	});

	// marked hands a blank line over as a token of its own, so a block's raw
	// may or may not end in a newline.
	it.each([
		[
			'a paragraph above a blank line',
			'one\ntwo\n\nnext\n',
			[
				['', 1, 2],
				['', 4, 4]
			]
		],
		[
			'a fence above a blank line',
			'```\ncode\n```\n\nnext\n',
			[
				['', 1, 3],
				['', 5, 5]
			]
		],
		[
			'a list above a blank line',
			'- a\n- b\n\nnext\n',
			[
				['', 1, 2],
				['', 4, 4]
			]
		],
		[
			'a heading underlined with ===',
			'Title\n===\n\nnext\n',
			[
				['Title', 1, 2],
				['Title', 4, 4]
			]
		],
		['the last paragraph of a file with no final newline', 'one\ntwo', [['', 1, 2]]]
	])('ends %s on its real last line', (_, src, want) => {
		expect(shape(src)).toEqual(want);
	});

	it('keeps each block’s own markdown', () => {
		expect(docBlocks('# A\n\n**bold** text\n', 'plan.md').map((b) => b.raw.trim())).toEqual([
			'# A',
			'**bold** text'
		]);
	});

	it('splits any other file on its blank lines, each run a listing', () => {
		const blocks = docBlocks('a: 1\nb: 2\n\nc: 3\n', 'cfg.yaml');
		expect(blocks).toEqual([
			{ raw: '```yaml\na: 1\nb: 2\n```', heading: '', startLine: 1, endLine: 2 },
			{ raw: '```yaml\nc: 3\n```', heading: '', startLine: 4, endLine: 4 }
		]);
	});

	it.each([
		[
			'several blank lines',
			'a\n\n\n\nb\n',
			[
				[1, 1],
				[5, 5]
			]
		],
		['leading blank lines', '\n\na\n', [[3, 3]]],
		[
			'a line of only spaces',
			'a\n   \nb\n',
			[
				[1, 1],
				[3, 3]
			]
		],
		[
			'windows line endings',
			'a\r\n\r\nb\r\n',
			[
				[1, 1],
				[3, 3]
			]
		],
		['no final newline', 'a\nb', [[1, 2]]],
		['an empty file', '', []],
		['a blank file', '\n\n\n', []]
	])('counts lines through %s', (_, src, want) => {
		expect(docBlocks(src, 'a.txt').map((b) => [b.startLine, b.endLine])).toEqual(want);
	});
});

describe('textOffset and rangeAt', () => {
	const dom = () => {
		const root = document.createElement('div');
		root.innerHTML = '<p>Hello <b>big</b> world</p><p>tail</p>';
		const [hello, big, world, tail] = ['Hello ', 'big', ' world', 'tail'].map((s) => {
			const walk = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
			for (let n = walk.nextNode(); n; n = walk.nextNode()) if (n.nodeValue === s) return n;
			throw new Error(`no text ${s}`);
		});
		return { root, hello, big, world, tail };
	};

	it('counts the text before a point, across elements', () => {
		const { root, hello, big, world, tail } = dom();
		expect(textOffset(root, hello, 0)).toBe(0);
		expect(textOffset(root, hello, 3)).toBe(3);
		expect(textOffset(root, big, 1)).toBe(7);
		expect(textOffset(root, world, 1)).toBe(10);
		expect(textOffset(root, tail, 4)).toBe(19);
	});

	it('falls back to the head for a node outside the root', () => {
		const { root } = dom();
		expect(textOffset(root, document.createTextNode('stale'), 2)).toBe(0);
	});

	it.each([
		['inside one text node', 0, 5, 'Hello'],
		['exactly one element', 6, 9, 'big'],
		['across text nodes', 4, 8, 'o bi'],
		['across paragraphs', 11, 16, 'orldt'],
		['to the very end', 15, 19, 'tail'],
		['past the end, cut there', 15, 99, 'tail'],
		['an empty stretch', 3, 3, '']
	])('covers %s', (_, start, end, want) => {
		expect(rangeAt(dom().root, start, end)?.toString()).toBe(want);
	});

	it('finds no range in a root with no text, or past all of it', () => {
		expect(rangeAt(document.createElement('div'), 0, 3)).toBeNull();
		expect(rangeAt(dom().root, 40, 50)).toBeNull();
	});

	it('turns a selection into offsets and back', () => {
		const { root, hello, tail } = dom();
		document.body.append(root);
		const picked = select(hello, 2, tail, 2);
		const round = rangeAt(
			root,
			textOffset(root, picked.startContainer, picked.startOffset),
			textOffset(root, picked.endContainer, picked.endOffset)
		);
		expect(round?.toString()).toBe(picked.toString());
		root.remove();
		window.getSelection()!.removeAllRanges();
	});
});

describe('locate', () => {
	const blocks = [
		aBlock({ startLine: 1, endLine: 1 }),
		aBlock({ startLine: 3, endLine: 4 }),
		aBlock({ startLine: 6, endLine: 6 })
	];
	const texts = ['Intro words', 'alpha beta\ngamma words', 'Final words'];
	const on = (quote: string, startLine: number, endLine = startLine) => ({
		quote,
		startLine,
		endLine
	});

	it.each([
		['in its block', on('alpha', 3, 4), [1, 0, 1, 5]],
		['past a line break the selection flattened', on('beta gamma', 3, 4), [1, 6, 1, 16]],
		['with the quote’s own whitespace loose', on('  beta\n\t gamma ', 3, 4), [1, 6, 1, 16]],
		['across two blocks', on('gamma words Final', 3, 6), [1, 11, 2, 5]],
		['in the block of its lines, when the words repeat', on('words', 6), [2, 6, 2, 11]],
		['anywhere, when its lines hold no block', on('Final', 99), [2, 0, 2, 5]]
	])('finds a quote %s', (_, comment, [startBlock, startOffset, endBlock, endOffset]) => {
		expect(locate(texts, blocks, comment)).toEqual({
			startBlock,
			startOffset,
			endBlock,
			endOffset
		});
	});

	it('finds a quote past a block’s leading whitespace', () => {
		const found = locate(['  \n  indented text'], [aBlock()], on('indented', 1));
		expect(found).toEqual({ startBlock: 0, startOffset: 5, endBlock: 0, endOffset: 13 });
	});

	it.each([
		['a quote that is not there', texts, blocks, on('absent', 1, 6)],
		['a quote outside its lines', texts, blocks, on('Final', 1)],
		['an empty quote', texts, blocks, on('', 1, 6)],
		['a quote of only spaces', texts, blocks, on(' \n ', 1, 6)],
		['a file without blocks', [], [], on('alpha', 1)],
		['a block whose text never rendered', [], blocks, on('alpha', 3, 4)]
	])('finds nothing for %s', (_, t, b, comment) => {
		expect(locate(t, b, comment)).toBeNull();
	});
});

describe('drafts', () => {
	beforeEach(() => localStorage.clear());

	it('loads nothing when nothing was saved', () => {
		expect(loadDraft('p1', 'plan.md')).toEqual({ comments: [], summary: '' });
	});

	it('gets back what it saved', () => {
		const comment = aComment({ startLine: 3, endLine: 4, quote: 'q' });
		saveDraft('p1', { comments: [comment], summary: 'Fine' });
		expect(loadDraft('p1', 'plan.md')).toEqual({ comments: [comment], summary: 'Fine' });
	});

	it('keeps each ping’s draft apart', () => {
		saveDraft('p1', { comments: [], summary: 'one' });
		expect(loadDraft('p2', 'plan.md').summary).toBe('');
	});

	it('drops attachments: a File does not survive JSON', () => {
		const files = [new File(['x'], 'x.txt')];
		saveDraft('p1', { comments: [aComment({ files })], summary: '' });
		expect(loadDraft('p1', 'plan.md').comments[0].files).toBeUndefined();
	});

	it('reads an old bare list as comments on the primary file', () => {
		localStorage.setItem(KEY, JSON.stringify([{ text: 'old', startLine: 2 }]));
		const { comments, summary } = loadDraft('p1', 'primary.md');
		expect(summary).toBe('');
		expect(comments).toHaveLength(1);
		expect(comments[0]).toMatchObject({
			path: 'primary.md',
			text: 'old',
			startLine: 2,
			quote: '',
			heading: '',
			endLine: 0
		});
		expect(comments[0].id).not.toBe('');
	});

	it('keeps the path a comment names', () => {
		localStorage.setItem(KEY, JSON.stringify({ comments: [{ path: 'other.md' }] }));
		expect(loadDraft('p1', 'primary.md').comments[0].path).toBe('other.md');
	});

	it.each([
		['garbage', '{not json'],
		['a bare value', '7'],
		['null', 'null'],
		['comments that are no list', '{"comments":"x"}']
	])('reads %s as an empty draft', (_, raw) => {
		localStorage.setItem(KEY, raw);
		expect(loadDraft('p1', 'plan.md')).toEqual({ comments: [], summary: '' });
	});

	it.each([
		['no comments and no summary', ''],
		['no comments and a blank summary', '  \n']
	])('clears the stored draft on %s', (_, summary) => {
		localStorage.setItem(KEY, '[]');
		saveDraft('p1', { comments: [], summary });
		expect(localStorage.getItem(KEY)).toBeNull();
	});

	it('clearDraft forgets a draft', () => {
		saveDraft('p1', { comments: [], summary: 'Fine' });
		clearDraft('p1');
		expect(localStorage.getItem(KEY)).toBeNull();
	});

	it('shrugs off a browser that refuses storage', () => {
		const refuse = () => {
			throw new Error('denied');
		};
		vi.spyOn(Storage.prototype, 'getItem').mockImplementation(refuse);
		vi.spyOn(Storage.prototype, 'setItem').mockImplementation(refuse);
		vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(refuse);

		expect(loadDraft('p1', 'plan.md')).toEqual({ comments: [], summary: '' });
		expect(() => saveDraft('p1', { comments: [], summary: 'x' })).not.toThrow();
		expect(() => saveDraft('p1', { comments: [], summary: '' })).not.toThrow();
		expect(() => clearDraft('p1')).not.toThrow();
	});
});
