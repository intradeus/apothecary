import { describe, expect, it } from 'vitest';
import { docBlocks, isRendered } from '$lib/review/reviewDoc';
import { structuredBlocks } from '$lib/review/structured';

const VERDICT = `verdict: REJECT
summary: |
  Two problems.
comments:
  - the change has no test
  - path: a.go
    line: 20
    end_line: 24
    quote: if err != nil
    body: wrap it
`;

const THREADS = `merge_request: https://x/mr/1
threads:
  - id: t1
    path: a.go
    line: 3
    resolved: false
    ours: false
    hunk: |
      @@ -1 +1 @@
      -a
      +b
    comments:
      - id: c1
        author: ana
        new: true
        body: Rename it.
  - id: t2
    resolved: true
    ours: true
    comments:
      - id: c2
        author: factory
        ours: true
        did: fixed
        body: Renamed.
dropped:
  - path: b.go
    line: 9
    kind: nit
    body: never mind
`;

describe('a verdict', () => {
	it('reads as its decision, summary and comments, each on its own lines', () => {
		expect(structuredBlocks(VERDICT, 'verdict')).toEqual([
			{ raw: '## Rejected', heading: 'Rejected', startLine: 1, endLine: 1 },
			{ raw: 'Two problems.', heading: 'Rejected', startLine: 2, endLine: 3 },
			{ raw: '### Comments (2)', heading: 'Comments (2)', startLine: 4, endLine: 4 },
			{ raw: '**1.** the change has no test', heading: 'Comments (2)', startLine: 5, endLine: 5 },
			{
				raw: '**2.** `a.go` · lines 20–24\n\n> if err != nil\n\nwrap it',
				heading: 'a.go',
				startLine: 6,
				endLine: 10
			}
		]);
	});

	it('reads as JSON too, a comment on one line or the whole file', () => {
		const src =
			'{"verdict": "APPROVE",\n "comments": [{"path": "b.go", "line": 7, "body": "nice"},\n {"path": "c.go", "body": "fine"}]}\n';
		expect(structuredBlocks(src, 'verdict')?.map((b) => [b.raw, b.startLine, b.endLine])).toEqual([
			['## Approved', 1, 1],
			['### Comments (2)', 2, 2],
			['**1.** `b.go` · line 7\n\nnice', 2, 2],
			['**2.** `c.go` · the whole file\n\nfine', 3, 3]
		]);
	});

	it.each([
		['no decision', 'summary: hm\n'],
		['another decision', 'verdict: MAYBE\n'],
		['not YAML', 'verdict: [REJECT\n'],
		['not a mapping', '- REJECT\n']
	])('is not one with %s', (_, src) => {
		expect(structuredBlocks(src, 'verdict')).toBeNull();
	});
});

describe('merge request threads', () => {
	it('read as each thread, its hunk, and its comments quoted', () => {
		const t1 = 'a.go:3 · thread t1';
		const t2 = "conversation · thread t2 · resolved · apothecary's";
		expect(structuredBlocks(THREADS, 'mr_threads')).toEqual([
			{ raw: 'Merge request: https://x/mr/1', heading: '', startLine: 1, endLine: 1 },
			{
				raw: '### `a.go:3` · thread t1\n\n````diff\n@@ -1 +1 @@\n-a\n+b\n````',
				heading: t1,
				startLine: 3,
				endLine: 12
			},
			{ raw: '**ana** · new\n\n> Rename it.', heading: t1, startLine: 13, endLine: 16 },
			{ raw: `### ${t2}`, heading: t2, startLine: 17, endLine: 20 },
			{
				raw: '**factory** · apothecary · said it fixed\n\n> Renamed.',
				heading: t2,
				startLine: 21,
				endLine: 25
			},
			{ raw: '### Dropped earlier', heading: 'Dropped earlier', startLine: 26, endLine: 26 },
			{
				raw: '`b.go:9` · nit\n\n> never mind',
				heading: 'Dropped earlier',
				startLine: 27,
				endLine: 30
			}
		]);
	});

	it('says so when there are none', () => {
		expect(structuredBlocks('threads: []\n', 'mr_threads')).toEqual([
			{ raw: 'No threads.', heading: '', startLine: 1, endLine: 1 }
		]);
	});

	it.each([
		['threads that are not a list', 'threads: many\n'],
		['a thread that is not a mapping', 'threads: [t1]\n']
	])('are not with %s', (_, src) => {
		expect(structuredBlocks(src, 'mr_threads')).toBeNull();
	});
});

describe('docBlocks of a typed file', () => {
	it('renders a type with a view of its own', () => {
		expect(docBlocks(VERDICT, 'artifacts/verdict.yaml', 'verdict')[0].raw).toBe('## Rejected');
	});

	it('shows a document that is not its type as written', () => {
		expect(docBlocks('verdict: MAYBE\n', 'v.yaml', 'verdict')).toEqual([
			{ raw: '```yaml\nverdict: MAYBE\n```', heading: '', startLine: 1, endLine: 1 }
		]);
	});

	it('shows a type with no view of its own as written', () => {
		expect(docBlocks('{"subject": "x"}\n', 'c.json', 'commit_message')[0].raw).toBe(
			'```json\n{"subject": "x"}\n```'
		);
	});

	it.each([
		['plan.md', undefined, true],
		['artifacts/verdict.yaml', 'verdict', true],
		['artifacts/threads.yaml', 'mr_threads', true],
		['artifacts/commit.json', 'commit_message', false],
		['cfg.yaml', undefined, false]
	])('renders %s (%s): %s', (path, type, want) => {
		expect(isRendered({ path, type })).toBe(want);
	});
});
