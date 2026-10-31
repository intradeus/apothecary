<script lang="ts">
	// ---- schemas: the structures the flow's own files hold (FLOWLANG §4) ---
	// A schema's panel: its name, its body as YAML, and the ports naming it.
	import type { Selection } from '$lib/flow/FlowGraph.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import YamlEditor from '$lib/flow/YamlEditor.svelte';
	import Caption from '$lib/flow/inspector/Caption.svelte';
	import Group from '$lib/flow/inspector/Group.svelte';
	import PanelHead from '$lib/flow/inspector/PanelHead.svelte';
	import type { SchemaDraft } from '$lib/flow/inspector/drafts.svelte';
	import { renamed } from '$lib/flow/inspector/inspector';
	import {
		SCHEMA_NAME,
		renameSchema,
		schemaUsers,
		type DocSchema,
		type FlowDoc
	} from '$lib/domain/flowdoc';

	let {
		doc,
		schema: q,
		draft,
		onchange,
		onselect,
		onopen,
		ondelete
	}: {
		doc: FlowDoc;
		schema: DocSchema;
		/** The body as typed: the inspector's, it outlives this panel. */
		draft: SchemaDraft;
		onchange: () => void;
		onselect: (sel: Selection, how?: 'rename') => void;
		onopen: (sel: Selection) => void;
		ondelete: (name: string) => void;
	} = $props();

	const users = $derived(schemaUsers(doc, q.name));

	function renameSchemaTo(from: string, to: string): boolean {
		to = to.trim();
		if (!to || to === from || !SCHEMA_NAME.test(to) || doc.schemas?.some((q) => q.name === to))
			return false;
		renameSchema(doc, from, to);
		onchange();
		onselect({ kind: 'schema', name: to }, 'rename');
		return true;
	}
</script>

<PanelHead id={q.name}>
	{#snippet lead()}
		<span class="pill sm flat sq mono tint-attention">{t('flow.insp.kind.schema')}</span>
	{/snippet}
	<button
		class="btn-danger btn-sm"
		disabled={users.length > 0}
		onclick={() => ondelete(q.name)}
		title={users.length ? t('flow.insp.schemaInUse') : t('flow.insp.deleteSchema')}
		>{t('flow.insp.delete')}</button
	>
</PanelHead>
<div class="help">{t('flow.insp.schemaPanelHelp')}</div>
<div class="field">
	<label for="sc-name">{t('flow.insp.schemaName')}</label>
	<input
		id="sc-name"
		class="mono"
		value={q.name}
		onchange={(e) =>
			renamed(e.currentTarget, q.name, renameSchemaTo(q.name, e.currentTarget.value))}
	/>
	<div class="help">{t('flow.insp.schemaRenameHelp')}</div>
</div>
<div class="field">
	<label for="sc-desc">{t('flow.insp.description')}</label>
	<input
		id="sc-desc"
		value={q.description ?? ''}
		placeholder={t('flow.insp.schemaDescPlaceholder')}
		oninput={(e) => {
			q.description = e.currentTarget.value || undefined;
			onchange();
		}}
	/>
</div>
<div class="field">
	<Caption text={t('flow.insp.schemaBody')} />
	<div class="schema-body card-in">
		<YamlEditor bind:value={draft.text} label={t('flow.insp.schemaBody')} />
	</div>
	{#if draft.error}
		<div class="help hue-warning">
			{t('flow.insp.schemaBodyInvalid', { error: draft.error })}
		</div>
	{/if}
</div>
<Group caption={t('flow.insp.schemaUsedBy')}>
	{#if users.length}
		<div class="chips">
			{#each users as u, i (i)}{#if 'agent' in u}<button
						class="btn-sm mono"
						onclick={() => onopen({ kind: 'agent', name: u.agent })}>{u.agent}.{u.port}</button
					>{:else}<button
						class="btn-sm mono"
						onclick={() => onopen({ kind: 'stage', id: u.stage, sequence: u.sequence })}
						>{u.stage}.{u.port}</button
					>{/if}{/each}
		</div>
	{:else}
		<div class="help">{t('flow.insp.schemaUnused')}</div>
	{/if}
</Group>

<style>
	.schema-body {
		height: 260px;
		padding: 0;
		overflow: hidden;
	}
</style>
