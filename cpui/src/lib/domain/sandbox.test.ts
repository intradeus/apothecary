import { describe, expect, it } from 'vitest';
import {
	aboveCeiling,
	idleAfter,
	IDLE_NEVER,
	effectiveRuntime,
	changedFields,
	effectiveSize,
	outOfBounds,
	runtimeProblem,
	sleepRefusal,
	sizeCap,
	sizeFieldValid,
	unsetSize,
	usageSummary,
	type SandboxRunUsage
} from './sandbox';

describe('sizeFieldValid', () => {
	it('takes unset or a number within its bounds', () => {
		expect(sizeFieldValid(null, 'cpus')).toBe(true);
		expect(sizeFieldValid(0.1, 'cpus')).toBe(true);
		expect(sizeFieldValid(0.5, 'memoryGiB')).toBe(true);
		expect(sizeFieldValid(16384, 'memoryGiB')).toBe(true);
	});

	it('refuses zero, a negative, NaN, and a field past its bounds', () => {
		expect(sizeFieldValid(0, 'cpus')).toBe(false);
		expect(sizeFieldValid(-1, 'cpus')).toBe(false);
		expect(sizeFieldValid(Number.NaN, 'memoryGiB')).toBe(false);
		expect(sizeFieldValid(0.05, 'cpus')).toBe(false);
		expect(sizeFieldValid(0.25, 'memoryGiB')).toBe(false);
		expect(sizeFieldValid(1e9, 'memoryGiB')).toBe(false);
	});

	it('names the fields out of bounds', () => {
		expect(outOfBounds({ cpus: 0.01, memoryGiB: 4 })).toEqual(['cpus']);
		expect(outOfBounds(unsetSize())).toEqual([]);
	});
});

describe('changedFields', () => {
	it('keeps only what differs', () => {
		expect(changedFields({ cpus: 8, memoryGiB: 16 }, { cpus: 8, memoryGiB: 4 })).toEqual({
			cpus: null,
			memoryGiB: 4
		});
		expect(changedFields(unsetSize(), { cpus: 2, memoryGiB: null })).toEqual({
			cpus: 2,
			memoryGiB: null
		});
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

	it('caps an unset field by the ceiling, never sizes it at it', () => {
		const ceilingOnly = {
			default: unsetSize(),
			ceiling: { cpus: null, memoryGiB: 64 },
			idleMinutes: 0
		};
		const eff = effectiveSize(unsetSize(), ceilingOnly);
		expect(eff).toEqual(unsetSize());
		expect(sizeCap(eff, ceilingOnly)).toEqual({ cpus: null, memoryGiB: 64 });
		expect(sizeCap({ cpus: 2, memoryGiB: 4 }, policy)).toEqual(unsetSize());
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

	it("says why idle sandboxes cannot be kept: the provisioner's storage, else the pick's runtime", () => {
		const vm = {
			...offer,
			approved: [...offer.approved, { name: 'kata', capabilities: caps(true), noSleep: 'a VM' }]
		};
		expect(sleepRefusal(vm, '')).toBeUndefined();
		expect(sleepRefusal(vm, 'kata')).toBe('a VM');
		expect(sleepRefusal({ ...vm, default: 'kata' }, '')).toBe('a VM');
		expect(sleepRefusal({ ...vm, noSleep: 'emptydir' }, '')).toBe('emptydir');
		expect(sleepRefusal(vm, 'gone')).toBeUndefined();
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
