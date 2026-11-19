import { describe, expect, it } from 'vitest';
import { MCP_PURPOSES } from './user';

describe('MCP_PURPOSES', () => {
	it('offers reading first, then each purpose once, as the engine names them', () => {
		expect(MCP_PURPOSES).toEqual(['read', 'tickets', 'runs', 'authoring']);
		expect(new Set(MCP_PURPOSES).size).toBe(MCP_PURPOSES.length);
	});
});
