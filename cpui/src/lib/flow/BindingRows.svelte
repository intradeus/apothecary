<script lang="ts">
	// A stage's free-form bindings (a command's reads / writes, a gate's
	// shows, bindings no contract declares) as editable name → path rows
	// (KeyValueEditor). Rows live here while they are being typed and are
	// written to the document as they change: only complete rows (a name and
	// a path) reach it, in the order shown, after any entries `keep` holds
	// back (declared bindings edited elsewhere stay where they are).
	//
	// Mounted once per selection (the inspector keys its content on it), so
	// its rows always belong to one stage.
	import { untrack } from 'svelte';
	import KeyValueEditor, { type KVRow } from '$lib/ui/KeyValueEditor.svelte';
	import type { DocStage } from '$lib/flowdoc';

	let {
		stage,
		field,
		keep = () => false,
		valuePlaceholder = 'artifacts/file.md',
		addLabel = 'Add',
		onchange
	}: {
		stage: DocStage;
		field: 'input' | 'output';
		/** Entries this editor leaves alone (and doesn't show). */
		keep?: (name: string) => boolean;
		valuePlaceholder?: string;
		addLabel?: string;
		onchange: () => void;
	} = $props();

	let rows = $state<KVRow[]>(
		untrack(() =>
			Object.entries(stage[field] ?? {})
				.filter(([k]) => !keep(k))
				.map(([key, value]) => ({ key, value }))
		)
	);

	const invalid = $derived.by(() => {
		const seen = new Set<string>();
		const bad = new Set<string>();
		for (const r of rows) {
			const k = r.key.trim();
			if (!k) continue;
			if (seen.has(k) || keep(k)) bad.add(k);
			seen.add(k);
		}
		return bad;
	});

	function write(next: KVRow[]) {
		rows = next;
		const kept = Object.entries(stage[field] ?? {}).filter(([k]) => keep(k));
		const rec: Record<string, string> = Object.fromEntries(kept);
		for (const r of next) {
			const k = r.key.trim();
			const v = r.value.trim();
			if (k && v && !(k in rec)) rec[k] = v;
		}
		stage[field] = Object.keys(rec).length ? rec : undefined;
		onchange();
	}
</script>

<KeyValueEditor
	bind:rows={() => rows, write}
	keyPlaceholder="name"
	{valuePlaceholder}
	keyWidth={110}
	{addLabel}
	{invalid}
/>
