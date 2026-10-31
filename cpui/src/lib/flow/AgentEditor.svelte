<script lang="ts">
	// One agent the flow carries (FLOWLANG §1), in the Agents tab: its prompt
	// with the room it needs and a preview of the sections a stage appends to
	// it, beside its settings and its contract — what it reads and writes,
	// typed, never where — and the flow's MCP servers it may call.
	import Select from '$lib/ui/Select.svelte';
	import Toggle from '$lib/ui/Toggle.svelte';
	import { I } from '$lib/ui/icons';
	import { STAGE_HUE } from '$lib/ui/hues';
	import { t } from '$lib/i18n/index.svelte';
	import {
		FILE_TYPES,
		MCP_BUILTINS,
		VALUE_TYPES,
		addMCPServer,
		mcpKind,
		takesSchema,
		type DocAgent,
		type DocMCPServer,
		type FlowDoc
	} from '$lib/flowdoc';
	import {
		addPort,
		deleteAgent,
		removePort,
		renameAgentTo,
		renamePort,
		runnersOf,
		type AgentField,
		type AgentRunner
	} from '$lib/flow/agents';

	let {
		doc,
		agent,
		onchange,
		onrenamed,
		ondeleted,
		onopenstage,
		onopenmcp
	}: {
		doc: FlowDoc;
		agent: DocAgent;
		onchange: () => void;
		/** After a rename took: the agent's new name. */
		onrenamed?: (to: string) => void;
		/** After the agent was deleted. */
		ondeleted?: () => void;
		/** A stage running it was picked. */
		onopenstage?: (r: AgentRunner) => void;
		/** An MCP server was picked, to edit it. */
		onopenmcp?: (name: string) => void;
	} = $props();

	const a = $derived(agent);
	const used = $derived(runnersOf(doc, a.name));
	const schemaNames = $derived((doc.schemas ?? []).map((q) => q.name));
	const schemaChoices = (current?: string) =>
		current && !schemaNames.includes(current) ? [...schemaNames, current] : schemaNames;
	const id = (k: string) => `ag-${k}`;

	// A refused rename puts the old text back.
	const renamed = (el: HTMLInputElement, current: string, took: boolean) => {
		if (!took) el.value = current;
	};
	function rename(el: HTMLInputElement) {
		const from = a.name;
		const took = renameAgentTo(doc, from, el.value);
		renamed(el, from, took);
		if (took) {
			onchange();
			onrenamed?.(a.name);
		}
	}
	async function remove() {
		if (await deleteAgent(doc, a.name)) {
			onchange();
			ondeleted?.();
		}
	}
	function add(field: AgentField) {
		addPort(a, field);
		onchange();
	}
	function drop(field: AgentField, name: string) {
		removePort(a, field, name);
		onchange();
	}

	// The MCP servers it may call: any of the flow's, toggled by name. One it
	// lists that the flow does not declare is shown so it can be dropped.
	const servers = $derived(doc.mcp_servers ?? []);
	const undeclared = $derived((a.mcp ?? []).filter((n) => !servers.some((m) => m.name === n)));
	function setMCP(name: string, on: boolean) {
		const rest = (a.mcp ?? []).filter((n) => n !== name);
		const next = on ? [...rest, name] : rest;
		a.mcp = next.length ? next : undefined;
		onchange();
	}
	function newServer() {
		const m = addMCPServer(doc);
		a.mcp = [...(a.mcp ?? []), m.name];
		onchange();
		onopenmcp?.(m.name);
	}
	// What a server is, when it says nothing of itself.
	const reach = (m: DocMCPServer) => {
		const kind = mcpKind(m);
		if (kind === 'builtin')
			return MCP_BUILTINS.includes(m.builtin ?? '')
				? t(`flow.insp.mcpBuiltin.${m.builtin}` as 'flow.insp.mcpBuiltin.browser')
				: (m.builtin ?? '');
		if (kind === 'url') return m.url ?? '';
		return (m.command ?? []).join(' ');
	};

	// What a stage running the agent appends to the prompt, per declared
	// port (the engine's assemblePrompt): the ticket as the TASK, each input
	// as an INPUT section, the outputs as the OUTPUT CONTRACT — and, from
	// the harness, the standing ASKING THE HUMAN rules (FLOWLANG §9): how
	// ask_human works, so the prompt says only when to ask.
	const appended = $derived.by(() => {
		const lines: string[] = [];
		for (const d of a.inputs ?? []) {
			if (d.type === 'ticket') lines.push(t('flow.agents.addsTask'));
			else if (d.type === 'failure' || d.type === 'verdict')
				lines.push(`# INPUT: ${d.name} — ${t('flow.agents.addsFeed', { type: d.type })}`);
			else if (d.type === 'changeset')
				lines.push(`# INPUT: ${d.name} — ${t('flow.agents.addsChangeset')}`);
			else if (d.type === 'link') lines.push(`# INPUT: ${d.name} — ${t('flow.agents.addsLink')}`);
			else if (d.type === 'files') lines.push(`# INPUT: ${d.name} — ${t('flow.agents.addsFiles')}`);
			else lines.push(`# INPUT: ${d.name} — ${t('flow.agents.addsFile')}`);
		}
		if (a.outputs?.length) {
			lines.push('# OUTPUT CONTRACT');
			for (const d of a.outputs)
				lines.push(
					`- <path${d.type === 'files' ? '/' : ''}> — ${d.name}: ${d.schema ?? d.type ?? 'file'}`
				);
		}
		lines.push(t('flow.agents.addsAskHuman'));
		return lines;
	});
	const promptStats = $derived.by(() => {
		const p = a.prompt ?? '';
		return {
			lines: p ? p.split('\n').length : 0,
			words: p.trim() ? p.trim().split(/\s+/).length : 0
		};
	});
</script>

{#snippet header()}
	<div class="ih">
		<span class="pill sm flat sq mono t-{STAGE_HUE.agent}">{t('flow.insp.kind.agent')}</span>
		<input
			id={id('name')}
			class="mono name-in"
			value={a.name}
			aria-label={t('flow.insp.agentName')}
			title={t('flow.insp.agentRenameHelp')}
			onchange={(e) => rename(e.currentTarget)}
		/>
		<span class="spacer"></span>
		<span class="fnt tiny">{t('flow.insp.runByCount', { count: used.length })}</span>
		<button class="btn-danger btn-sm" onclick={remove} title={t('flow.insp.deleteAgent')}
			>{t('flow.insp.delete')}</button
		>
	</div>
{/snippet}

{#snippet settings()}
	<div class="field">
		<label for={id('desc')}>{t('flow.insp.description')}</label>
		<input
			id={id('desc')}
			value={a.description ?? ''}
			placeholder={t('flow.insp.agentDescPlaceholder')}
			oninput={(e) => {
				a.description = e.currentTarget.value || undefined;
				onchange();
			}}
		/>
	</div>
	<Toggle
		checked={!!a.read_only}
		label={t('flow.insp.readOnly')}
		title={t('flow.insp.readOnlyTitle')}
		onchange={(v) => {
			a.read_only = v || undefined;
			onchange();
		}}
	/>
	<Toggle
		checked={!!a.allow_bash}
		label={t('flow.insp.allowBash')}
		title={t('flow.insp.allowBashTitle')}
		onchange={(v) => {
			a.allow_bash = v || undefined;
			onchange();
		}}
	/>
{/snippet}

{#snippet prompt()}
	<div class="field prompt-field">
		<div class="row nowrap">
			<label for={id('prompt')} class="grow">{t('flow.insp.prompt')}</label>
			<span class="fnt tiny"
				>{t('flow.agents.promptStats', {
					lines: promptStats.lines,
					words: promptStats.words
				})}</span
			>
		</div>
		<textarea
			id={id('prompt')}
			class="mono prompt"
			rows="28"
			spellcheck="true"
			bind:value={a.prompt}
			oninput={onchange}
			placeholder={t('flow.insp.promptPlaceholder')}></textarea>
		<div class="help">{t('flow.insp.promptHelp')}</div>
	</div>
	<div class="field">
		<div class="lbl">{t('flow.agents.addsTitle')}</div>
		<div class="help">{t('flow.agents.addsHelp')}</div>
		<pre class="adds mono card-in">{appended.join('\n')}</pre>
	</div>
{/snippet}

{#snippet inputs()}
	<div class="grp">
		<div class="lbl">{t('flow.insp.inputs')}</div>
		<div class="help">{t('flow.insp.agentInputsHelp')}</div>
	</div>
	{#each a.inputs ?? [] as d, i (i)}
		<div class="io-bind card-in port">
			<div class="row nowrap">
				<input
					class="mono grow"
					value={d.name}
					aria-label={t('flow.insp.portName')}
					onchange={(e) => {
						const took = renamePort(doc, a, 'input', d.name, e.currentTarget.value);
						renamed(e.currentTarget, d.name, took);
						if (took) onchange();
					}}
				/>
				<button class="btn-sm btn-ghost" onclick={() => drop('input', d.name)}
					>{t('flow.insp.removePort')}</button
				>
			</div>
			<div class="row nowrap">
				<span class="grow">
					<Select
						value={d.type ?? ''}
						label={t('flow.insp.portType')}
						options={[
							{ value: '', label: t('flow.insp.noType') },
							...[...FILE_TYPES, ...VALUE_TYPES].map((ty) => ({ value: ty, label: ty }))
						]}
						onchange={(v) => {
							d.type = v || undefined;
							// A schema says what a file is: never beside `file`, a directory or a value.
							if (!takesSchema(d.type)) d.schema = undefined;
							onchange();
						}}
					/>
				</span>
				{#if (schemaNames.length || d.schema) && takesSchema(d.type)}
					<span class="grow">
						<Select
							value={d.schema ?? ''}
							label={t('flow.insp.portSchema')}
							title={t('flow.insp.inputSchemaTitle')}
							options={[
								{ value: '', label: t('flow.insp.noSchema') },
								...schemaChoices(d.schema).map((n) => ({ value: n, label: n }))
							]}
							onchange={(v) => {
								d.schema = v || undefined;
								onchange();
							}}
						/>
					</span>
				{/if}
			</div>
			<input
				value={d.description ?? ''}
				aria-label={t('flow.insp.description')}
				placeholder={t('flow.insp.portDescPlaceholder')}
				oninput={(e) => {
					d.description = e.currentTarget.value || undefined;
					onchange();
				}}
			/>
			<Toggle
				checked={!!d.optional}
				label={t('flow.insp.optional')}
				title={t('flow.insp.optionalTitle')}
				onchange={(v) => {
					d.optional = v || undefined;
					onchange();
				}}
			/>
		</div>
	{/each}
	<div class="chips">
		<button class="btn-sm" onclick={() => add('input')}>{I.add} {t('flow.insp.addInput')}</button>
	</div>
{/snippet}

{#snippet outputs()}
	<div class="grp">
		<div class="lbl">{t('flow.insp.outputs')}</div>
		<div class="help">{t('flow.insp.agentOutputsHelp')}</div>
	</div>
	{#each a.outputs ?? [] as d, i (i)}
		<div class="io-bind card-in port">
			<div class="row nowrap">
				<input
					class="mono grow"
					value={d.name}
					aria-label={t('flow.insp.portName')}
					onchange={(e) => {
						const took = renamePort(doc, a, 'output', d.name, e.currentTarget.value);
						renamed(e.currentTarget, d.name, took);
						if (took) onchange();
					}}
				/>
				<button class="btn-sm btn-ghost" onclick={() => drop('output', d.name)}
					>{t('flow.insp.removePort')}</button
				>
			</div>
			<div class="row nowrap">
				<span class="grow">
					<Select
						value={d.type ?? ''}
						label={t('flow.insp.portType')}
						options={[
							{ value: '', label: t('flow.insp.noType') },
							...FILE_TYPES.map((ty) => ({
								value: ty,
								label: t(`flow.insp.outputType.${ty}` as 'flow.insp.outputType.file')
							})),
							...(d.type && !FILE_TYPES.includes(d.type) ? [{ value: d.type, label: d.type }] : [])
						]}
						onchange={(v) => {
							d.type = v || undefined;
							// A schema says what a file is: never beside `file` or a directory.
							if (!takesSchema(d.type)) d.schema = undefined;
							onchange();
						}}
					/>
				</span>
				{#if (schemaNames.length || d.schema) && takesSchema(d.type)}
					<span class="grow">
						<Select
							value={d.schema ?? ''}
							label={t('flow.insp.portSchema')}
							title={t('flow.insp.outputSchemaTitle')}
							options={[
								{ value: '', label: t('flow.insp.noSchema') },
								...schemaChoices(d.schema).map((n) => ({ value: n, label: n }))
							]}
							onchange={(v) => {
								d.schema = v || undefined;
								onchange();
							}}
						/>
					</span>
				{/if}
			</div>
			<input
				value={d.description ?? ''}
				aria-label={t('flow.insp.description')}
				placeholder={t('flow.insp.portDescPlaceholder')}
				oninput={(e) => {
					d.description = e.currentTarget.value || undefined;
					onchange();
				}}
			/>
		</div>
	{/each}
	<div class="chips">
		<button class="btn-sm" onclick={() => add('output')}>{I.add} {t('flow.insp.addOutput')}</button>
	</div>
{/snippet}

{#snippet mcp()}
	<div class="grp">
		<div class="lbl">{t('flow.agents.mcp')}</div>
		<div class="help">{t('flow.agents.mcpHelp')}</div>
	</div>
	{#each servers as m (m.name)}
		<div class="row nowrap mcp-row">
			<Toggle
				checked={!!a.mcp?.includes(m.name)}
				label={m.name}
				title={t('flow.agents.mcpToggle', { name: m.name })}
				onchange={(v) => setMCP(m.name, v)}
			/>
			<span class="fnt small ell grow" title={m.description ?? reach(m)}
				>{m.description ?? reach(m)}</span
			>
			{#if onopenmcp}
				<button class="btn-sm btn-ghost" onclick={() => onopenmcp(m.name)}
					>{t('flow.agents.mcpEdit')}</button
				>
			{/if}
		</div>
	{/each}
	{#each undeclared as name (name)}
		<div class="row nowrap mcp-row">
			<span class="mono hue-gold grow">{name}</span>
			<span class="fnt small">{t('flow.agents.mcpUndeclared')}</span>
			<button class="btn-sm btn-ghost" onclick={() => setMCP(name, false)}
				>{t('flow.insp.removePort')}</button
			>
		</div>
	{/each}
	<div class="chips">
		<button class="btn-sm" onclick={newServer}>{I.add} {t('flow.insp.addMCP')}</button>
	</div>
{/snippet}

{#snippet runBy()}
	<div class="grp">
		<div class="lbl">{t('flow.insp.runBy')}</div>
		{#if used.length}
			<div class="chips">
				{#each used as u (u.label)}<button class="btn-sm mono" onclick={() => onopenstage?.(u)}
						>{u.label}</button
					>{/each}
			</div>
		{:else}
			<div class="help">{t('flow.insp.agentUnused')}</div>
		{/if}
	</div>
{/snippet}

<div class="wide">
	{@render header()}
	<div class="cols">
		<section class="main" aria-label={t('flow.insp.prompt')}>
			{@render prompt()}
		</section>
		<section class="contract" aria-label={t('flow.agents.contract')}>
			{@render settings()}
			{@render mcp()}
			{@render runBy()}
			{@render inputs()}
			{@render outputs()}
		</section>
	</div>
</div>

<style>
	.ih {
		display: flex;
		align-items: center;
		gap: 8px;
		padding-bottom: 10px;
		border-bottom: 1px solid var(--ln);
		min-width: 0;
	}
	.name-in {
		font-size: var(--fs-section);
		font-weight: 600;
		min-width: 0;
		flex: 0 1 320px;
	}
	.grp {
		display: flex;
		flex-direction: column;
		gap: 2px;
		margin-top: 4px;
	}
	.lbl {
		margin-top: 4px;
	}
	/* An agent's declared input or output: its name, then what describes it. */
	.port {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.io-bind {
		padding: 7px 9px;
	}
	.prompt {
		resize: vertical;
		min-height: 52vh;
		line-height: 1.6;
		font-size: var(--fs-body);
	}
	/* The Agents tab: the prompt where the eye starts, the contract beside it. */
	.wide {
		display: grid;
		gap: 14px;
		min-width: 0;
	}
	.cols {
		display: grid;
		grid-template-columns: minmax(0, 1.7fr) minmax(280px, 1fr);
		gap: 22px;
		align-items: start;
	}
	.main,
	.contract {
		display: grid;
		gap: 10px;
		min-width: 0;
	}
	.mcp-row {
		gap: 8px;
		min-width: 0;
	}
	.adds {
		margin: 0;
		padding: 10px 12px;
		white-space: pre-wrap;
		color: var(--mut);
		font-size: var(--fs-label);
	}
	@media (max-width: 900px) {
		.cols {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
