<script lang="ts">
	// ---- MCP servers: tools the flow's agents may call (FLOWLANG §1) -------
	// A server is reached one way — builtin, command or url — and given env
	// (a command) or headers (a url) whose values may read a project secret.
	import type { Selection } from '$lib/flow/FlowGraph.svelte';
	import Select from '$lib/ui/Select.svelte';
	import KeyValueEditor from '$lib/ui/KeyValueEditor.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import Caption from '$lib/flow/inspector/Caption.svelte';
	import Group from '$lib/flow/inspector/Group.svelte';
	import PanelHead from '$lib/flow/inspector/PanelHead.svelte';
	import type { MCPDraft } from '$lib/flow/inspector/drafts.svelte';
	import { renamed } from '$lib/flow/inspector/inspector';
	import {
		MCP_BUILTINS,
		MCP_KINDS,
		MCP_NAME,
		mcpKind,
		mcpUsers,
		renameMCPServer,
		type DocMCPServer,
		type FlowDoc,
		type MCPKind
	} from '$lib/domain/flowdoc';

	let {
		doc,
		server: m,
		draft,
		onchange,
		onselect,
		onopen,
		ondelete
	}: {
		doc: FlowDoc;
		server: DocMCPServer;
		/** The command and the rows as typed: the inspector's, they outlive
		 *  this panel. */
		draft: MCPDraft;
		onchange: () => void;
		onselect: (sel: Selection, how?: 'rename') => void;
		onopen: (sel: Selection) => void;
		ondelete: (name: string) => void;
	} = $props();

	const users = $derived(mcpUsers(doc, m.name));
	const kind = $derived(mcpKind(m));

	function renameMCPTo(from: string, to: string): boolean {
		to = to.trim();
		if (!to || to === from || !MCP_NAME.test(to) || doc.mcp_servers?.some((m) => m.name === to))
			return false;
		renameMCPServer(doc, from, to);
		onchange();
		onselect({ kind: 'mcp', name: to }, 'rename');
		return true;
	}
	// Switching how a server is reached keeps what still applies: a
	// command's words and env, a url's address and headers.
	function setMCPKind(to: MCPKind) {
		if (to === mcpKind(m)) return;
		m.builtin = to === 'builtin' ? (m.builtin ?? MCP_BUILTINS[0]) : undefined;
		m.command = to === 'command' ? (m.command ?? []) : undefined;
		m.url = to === 'url' ? (m.url ?? '') : undefined;
		if (to !== 'command') m.env = undefined;
		if (to !== 'url') m.headers = undefined;
		onchange();
	}
	// A key the parser refuses: one given twice, or not a name — an
	// environment variable's, a header's.
	const ENV_KEY = /^[A-Za-z_][A-Za-z0-9_]*$/;
	const HEADER_KEY = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;
	const badKeys = $derived.by(() => {
		const re = mcpKind(m) === 'url' ? HEADER_KEY : ENV_KEY;
		const keys = draft.rows.map((r) => r.key.trim()).filter(Boolean);
		return new Set(keys.filter((k, i) => !re.test(k) || keys.indexOf(k) !== i));
	});
</script>

<PanelHead id={m.name}>
	{#snippet lead()}
		<span class="pill sm flat sq mono tint-accent-2">{t('flow.insp.kind.mcp')}</span>
	{/snippet}
	<button
		class="btn-danger btn-sm"
		onclick={() => ondelete(m.name)}
		title={t('flow.insp.deleteMCP')}>{t('flow.insp.delete')}</button
	>
</PanelHead>
<div class="help">{t('flow.insp.mcpPanelHelp')}</div>
<div class="field">
	<label for="mc-name">{t('flow.insp.schemaName')}</label>
	<input
		id="mc-name"
		class="mono"
		value={m.name}
		onchange={(e) => renamed(e.currentTarget, m.name, renameMCPTo(m.name, e.currentTarget.value))}
	/>
	<div class="help">{t('flow.insp.mcpRenameHelp')}</div>
</div>
<div class="field">
	<label for="mc-desc">{t('flow.insp.description')}</label>
	<input
		id="mc-desc"
		value={m.description ?? ''}
		placeholder={t('flow.insp.mcpDescPlaceholder')}
		oninput={(e) => {
			m.description = e.currentTarget.value || undefined;
			onchange();
		}}
	/>
</div>
<div class="field">
	<label for="mc-kind">{t('flow.insp.mcpKind')}</label>
	<Select
		id="mc-kind"
		value={kind}
		options={MCP_KINDS.map((k) => ({ value: k, label: t(`flow.insp.mcpKind.${k}`) }))}
		onchange={(v) => setMCPKind(v as MCPKind)}
	/>
</div>
{#if kind === 'builtin'}
	<div class="field">
		<label for="mc-builtin">{t('flow.insp.mcpBuiltin')}</label>
		<Select
			id="mc-builtin"
			value={m.builtin ?? ''}
			options={[
				...MCP_BUILTINS.map((b) => ({
					value: b,
					label: t(`flow.insp.mcpBuiltin.${b}` as 'flow.insp.mcpBuiltin.browser')
				})),
				...(m.builtin && !MCP_BUILTINS.includes(m.builtin)
					? [{ value: m.builtin, label: m.builtin }]
					: [])
			]}
			onchange={(v) => {
				m.builtin = v;
				onchange();
			}}
		/>
		<div class="help">{t('flow.insp.mcpBuiltinHelp')}</div>
	</div>
{:else if kind === 'command'}
	<div class="field">
		<label for="mc-command">{t('flow.insp.mcpCommand')}</label>
		<textarea
			id="mc-command"
			class="mono"
			rows="3"
			bind:value={draft.command}
			placeholder={'npx\n-y\n@modelcontextprotocol/server-github'}></textarea>
		<div class="help">{t('flow.insp.mcpCommandHelp')}</div>
	</div>
{:else}
	<div class="field">
		<label for="mc-url">{t('flow.insp.mcpUrl')}</label>
		<input
			id="mc-url"
			class="mono"
			type="url"
			value={m.url ?? ''}
			placeholder="https://mcp.example.com/"
			oninput={(e) => {
				m.url = e.currentTarget.value.trim();
				onchange();
			}}
		/>
		<div class="help">{t('flow.insp.mcpUrlHelp')}</div>
	</div>
{/if}
{#if kind !== 'builtin'}
	<div class="field">
		<Caption text={kind === 'url' ? t('flow.insp.mcpHeaders') : t('flow.insp.mcpEnv')} />
		<KeyValueEditor
			bind:rows={draft.rows}
			keyPlaceholder={kind === 'url' ? 'Authorization' : 'TOKEN'}
			valuePlaceholder={kind === 'url' ? 'Bearer ${secrets.name}' : '${secrets.name}'}
			keyWidth={120}
			addLabel={kind === 'url' ? t('flow.insp.mcpAddHeader') : t('flow.insp.mcpAddEnv')}
			invalid={badKeys}
		/>
		<div class="help">{t('flow.insp.mcpSecretsHelp')}</div>
	</div>
{/if}
<Group caption={t('flow.insp.mcpUsedBy')}>
	{#if users.length}
		<div class="chips">
			{#each users as name (name)}<button
					class="btn-sm mono"
					onclick={() => onopen({ kind: 'agent', name })}>{name}</button
				>{/each}
		</div>
	{:else}
		<div class="help">{t('flow.insp.mcpUnused')}</div>
	{/if}
</Group>
