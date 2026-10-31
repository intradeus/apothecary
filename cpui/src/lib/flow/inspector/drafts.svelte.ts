// What a panel holds as typed text before the document takes it. The panels
// come and go with the selection, but a draft outlives them: the inspector
// makes one of each once, so a rename — a new panel on the same schema or
// server — keeps a half-typed body instead of reloading it.
import { untrack } from 'svelte';
import { parse as parseYaml, stringify as stringifyYaml } from 'yaml';
import type { KVRow } from '$lib/ui/KeyValueEditor.svelte';
import { mcpKind, type DocMCPServer, type DocSchema } from '$lib/domain/flowdoc';

/** The schema's body is edited as YAML text; the document takes it only
 *  once it parses, so a half-typed line never reaches the draft. */
export class SchemaDraft {
	text = $state('');
	error = $state('');

	constructor(schema: () => DocSchema | undefined, onchange: () => void) {
		$effect(() => {
			const q = schema();
			untrack(() => {
				this.text = q ? stringifyYaml(q.json_schema ?? {}, { lineWidth: 0 }) : '';
				this.error = '';
			});
		});
		$effect(() => {
			const text = this.text;
			untrack(() => {
				const q = schema();
				if (!q) return;
				let v: unknown;
				try {
					v = parseYaml(text) ?? {};
				} catch (err) {
					this.error = err instanceof Error ? err.message.split('\n')[0] : String(err);
					return;
				}
				this.error = '';
				if (JSON.stringify(v) === JSON.stringify(q.json_schema)) return;
				q.json_schema = v;
				onchange();
			});
		});
	}
}

/** The command and the env or headers are typed as text and rows, and
 *  reach the document as they parse: a word per line, rows with a name. */
export class MCPDraft {
	command = $state('');
	rows = $state<KVRow[]>([]);

	constructor(server: () => DocMCPServer | undefined, onchange: () => void) {
		$effect(() => {
			const m = server();
			const kind = m ? mcpKind(m) : undefined;
			untrack(() => {
				this.command = m ? (m.command ?? []).join('\n') : '';
				const kv = kind === 'url' ? m?.headers : kind === 'command' ? m?.env : undefined;
				this.rows = Object.entries(kv ?? {}).map(([key, value]) => ({ key, value }));
			});
		});
		$effect(() => {
			const text = this.command;
			untrack(() => {
				const m = server();
				if (!m || mcpKind(m) !== 'command') return;
				const words = text
					.split('\n')
					.map((w) => w.trim())
					.filter(Boolean);
				if (JSON.stringify(words) === JSON.stringify(m.command ?? [])) return;
				m.command = words;
				onchange();
			});
		});
		$effect(() => {
			const rows = this.rows;
			untrack(() => {
				const m = server();
				const kind = m ? mcpKind(m) : 'builtin';
				if (!m || kind === 'builtin') return;
				const kv: Record<string, string> = {};
				for (const r of rows) if (r.key.trim()) kv[r.key.trim()] = r.value;
				const next = Object.keys(kv).length ? kv : undefined;
				const field = kind === 'url' ? 'headers' : 'env';
				if (JSON.stringify(next) === JSON.stringify(m[field])) return;
				m[field] = next;
				onchange();
			});
		});
	}
}
