// The last "Test" result per integration (DESIGN 4.17: test results
// persist). The engine does not record them, so this browser does: a
// convenience, tolerant of blocked storage.

export interface TestResult {
	ok: boolean;
	error?: string;
	/** ms epoch */
	at: number;
}

const KEY = 'apothecary.integrationTests';

export function readTests(): Record<string, TestResult> {
	try {
		return JSON.parse(localStorage.getItem(KEY) ?? '{}') as Record<string, TestResult>;
	} catch {
		return {};
	}
}

export function writeTest(id: string, r: TestResult | null) {
	try {
		const all = readTests();
		if (r) all[id] = r;
		else delete all[id];
		localStorage.setItem(KEY, JSON.stringify(all));
	} catch {
		/* storage blocked: the result lasts this page only */
	}
}
