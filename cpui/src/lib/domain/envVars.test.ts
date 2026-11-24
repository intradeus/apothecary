import { describe, expect, it } from 'vitest';
import {
	envFromRows,
	envIssues,
	envRows,
	MAX_ENV_VALUE_BYTES,
	MAX_ENV_VARS,
	tooManyEnvVars
} from './envVars';

const row = (name: string, value = 'x') => ({ name, value });

describe('envVars', () => {
	it('lists a stored map sorted, and saves rows back trimmed, dropping nameless ones', () => {
		expect(envRows({ NX_PARALLEL: '${APO_CPUS}', JEST_MAX_WORKERS: '2' })).toEqual([
			row('JEST_MAX_WORKERS', '2'),
			row('NX_PARALLEL', '${APO_CPUS}')
		]);
		expect(envRows(undefined)).toEqual([]);
		expect(envFromRows([row(' A ', '1'), row('', 'orphan'), row('B', '')])).toEqual({
			A: '1',
			B: ''
		});
	});

	it('refuses what the engine refuses, row by row', () => {
		expect(
			envIssues([
				row('JEST_MAX_WORKERS', '2'),
				row('1X'),
				row('MY-VAR'),
				row('apo_cpus'),
				row('APOTHECARY_RUN_ID'),
				row('PATH'),
				row('JEST_MAX_WORKERS'),
				row('BIG', 'é'.repeat(MAX_ENV_VALUE_BYTES / 2 + 1)),
				row('', ''),
				row('', 'value without a name')
			])
		).toEqual([
			undefined,
			{ kind: 'name' },
			{ kind: 'name' },
			{ kind: 'reservedPrefix', prefix: 'APO_' },
			{ kind: 'reservedPrefix', prefix: 'APOTHECARY_' },
			{ kind: 'reservedName' },
			{ kind: 'duplicate' },
			{ kind: 'tooLong' },
			undefined,
			{ kind: 'name' }
		]);
		expect(envIssues([row('BIG', 'x'.repeat(MAX_ENV_VALUE_BYTES))])).toEqual([undefined]);
	});

	it('counts the variables a save keeps against the bound', () => {
		const rows = Array.from({ length: MAX_ENV_VARS }, (_, i) => row(`V${i}`));
		expect(tooManyEnvVars(rows)).toBe(false);
		expect(tooManyEnvVars([...rows, row('')])).toBe(false);
		expect(tooManyEnvVars([...rows, row('ONE_MORE')])).toBe(true);
	});
});
