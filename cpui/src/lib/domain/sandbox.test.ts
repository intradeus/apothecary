import { describe, expect, it } from 'vitest';
import {
	aboveCeiling,
	idleAfter,
	IDLE_NEVER,
	effectiveRuntime,
	effectiveSize,
	runtimeProblem,
	sizeFieldValid,
	unsetSize,
	usageSummary,
	type SandboxRunUsage
} from './sandbox';

describe('sizeFieldValid', () => {
	it('takes unset or a positive number', () => {
		expect(sizeFieldValid(null)).toBe(true);
		expect(sizeFieldValid(0.5)).toBe(true);
	});

	it('refuses zero, a negative and NaN', () => {
		expect(sizeFieldValid(0)).toBe(false);
		expect(sizeFieldValid(-1)).toBe(false);
		expect(sizeFieldValid(Number.NaN)).toBe(false);
	});
});

describe('aboveCeiling', () => {
	const ceiling = { cpus: 4, memoryGiB: 8 };

	it('names each field above it', () => {
		expect(aboveCeiling({ cpus: 6, memoryGiB: 16 }, ceiling)).toEqual(['cpus', 'memoryGiB']);
		expect(aboveCeiling({ cpus: 4, memoryGiB: 9 }, ceiling)).toEqual(['memoryGiB']);
	});

	it('never counts an unset field, or one with no ceiling', () => {
		expect(aboveCeiling(unsetSize(), ceiling)).toEqual([]);
		expect(aboveCeiling({ cpus: 64, memoryGiB: 512 }, unsetSize())).toEqual([]);
	});
});

describe('effectiveSize', () => {
	const policy = {
		default: { cpus: 2, memoryGiB: 4 },
		ceiling: { cpus: 4, memoryGiB: 8 },
		idleMinutes: 0
	};

	it('takes the default for an unset field', () => {
		expect(effectiveSize({ cpus: null, memoryGiB: 6 }, policy)).toEqual({ cpus: 2, memoryGiB: 6 });
	});

	it('caps at the ceiling', () => {
		expect(effectiveSize({ cpus: 16, memoryGiB: 32 }, policy)).toEqual({ cpus: 4, memoryGiB: 8 });
	});

	it('bounds an unset field by the ceiling when there is no default', () => {
		expect(
			effectiveSize(unsetSize(), {
				default: unsetSize(),
				ceiling: { cpus: null, memoryGiB: 8 },
				idleMinutes: 0
			})
		).toEqual({ cpus: null, memoryGiB: 8 });
	});

	it('leaves everything unset without a policy', () => {
		expect(
			effectiveSize(unsetSize(), { default: unsetSize(), ceiling: unsetSize(), idleMinutes: 0 })
		).toEqual(unsetSize());
	});
});

describe('usageSummary', () => {
	const run = (peakMem: number, oomKills = 0): SandboxRunUsage => ({
		runId: 'r',
		peakMem,
		memLimit: 0,
		oomKills,
		at: '2026-11-22T10:00:00Z'
	});

	it('is the highest peak and the kills of every run', () => {
		expect(usageSummary([run(3), run(7, 1), run(5, 2)])).toEqual({
			peakMem: 7,
			oomKills: 3,
			runs: 3
		});
	});

	it('is null with no runs', () => {
		expect(usageSummary([])).toBeNull();
	});
});

describe('sandbox runtimes', () => {
	const caps = (nestedContainers: boolean) => ({
		nestedContainers,
		nestedKubernetes: false,
		userNamespaces: false,
		contained: true
	});
	const offer = {
		offered: true,
		default: 'sysbox',
		approved: [
			{ name: 'sysbox', capabilities: caps(true) },
			{ name: 'plain', capabilities: caps(false) }
		]
	};

	it('runs under the pick, else the default', () => {
		expect(effectiveRuntime(offer, '')).toBe('sysbox');
		expect(effectiveRuntime(offer, 'plain')).toBe('plain');
	});

	it('names what is wrong with a pick', () => {
		expect(runtimeProblem(offer, '', true)).toBeNull();
		expect(runtimeProblem(offer, 'plain', false)).toBeNull();
		expect(runtimeProblem(offer, 'plain', true)).toBe('noDocker');
		expect(runtimeProblem(offer, 'kata', false)).toBe('notApproved');
	});
});

describe('idleAfter', () => {
	it.each([
		[0, 30, 30],
		[0, 0, 0],
		[IDLE_NEVER, 30, 0],
		[45, 30, 45],
		[45, 0, 45]
	])('own %i under a default of %i is %i minutes', (own, def, want) => {
		expect(idleAfter(own, def)).toBe(want);
	});
});
