<script lang="ts">
	import { Handle, Position, useUpdateNodeInternals, type NodeProps } from '@xyflow/svelte';
	import StageChip from '$lib/ui/StageChip.svelte';
	import {
		isFeedSentinel,
		stageInputs,
		stageOutputs,
		type ActionContract,
		type AgentContract,
		type DocStage
	} from '$lib/flowdoc';

	let { id, data, selected }: NodeProps = $props();
	const stage = $derived(data.stage as DocStage);
	const entry = $derived(Boolean(data.entry));
	const agents = $derived((data.agents ?? []) as AgentContract[]);
	const actions = $derived((data.actions ?? []) as ActionContract[]);

	// Dataflow ports: one dot per artifact the stage reads (left) and writes
	// (right). Distinct from the colored control handles: these are NOT
	// rewireable — the wiring IS the artifact paths, so you change it by editing
	// an input or output, and the data edges follow.
	const inputs = $derived(stageInputs(stage, agents, actions));
	const outputs = $derived(stageOutputs(stage, agents, actions));

	// xyflow caches handle geometry per node id, and a node's id is stable while
	// its dots come and go (bindings change as the document is edited).
	// Telling it to re-measure is what keeps the data edges attached.
	const updateNodeInternals = useUpdateNodeInternals();
	const dotSignature = $derived(
		[
			...inputs.map((p) => `i:${p.name}:${p.value}`),
			...outputs.map((p) => `o:${p.name}:${p.value}`)
		].join('|')
	);
	$effect(() => {
		void dotSignature;
		updateNodeInternals(id);
	});

	// Stage-type hues (DESIGN 3.3), for the node's top accent.
	const HUE: Record<string, string> = {
		agent: 'var(--az)',
		command: 'var(--grn)',
		human_gate: 'var(--amb)',
		action: 'var(--vio)',
		wait: 'var(--tea)'
	};
	const detail = $derived(
		stage.type === 'agent'
			? stage.agent || '(no agent)'
			: stage.type === 'command'
				? stage.run_ref || (stage.run ? 'script' : '(no command)')
				: stage.type === 'action'
					? stage.action || '(no action)'
					: stage.type === 'wait'
						? `for ${stage.for || '(no signal)'}`
						: stage.question || 'asks a human'
	);
</script>

<div class="stage-node" class:selected style:--h={HUE[stage.type] ?? 'var(--mut)'}>
	<div class="head">
		<!-- The un-id'd target: every control edge lands here unless the target
         bound the value it carries, in which case it lands on that dot. -->
		<Handle type="target" position={Position.Left} class="h-in" style="top:20px" />
		<StageChip type={stage.type} />
		<span class="mono id">{stage.id}</span>
		{#if entry}<span class="entry" title="The flow (or its phase) starts here">ENTRY</span>{/if}
		<Handle
			id="success"
			type="source"
			position={Position.Right}
			class="h-success"
			style="top:20px"
		/>
	</div>
	<div class="meta" class:unset={detail.startsWith('(')}>{detail}</div>

	{#if inputs.length || outputs.length}
		<div class="ports">
			{#each inputs as p (p.name)}
				<!-- A feed sentinel ($DECISION/$FAILURE) gets a dot too: the control
             edge that carries it lands there, so the retry path is visible. -->
				<div
					class="io in"
					class:unbound={p.unbound}
					class:sentinel={p.sentinel}
					class:fed={isFeedSentinel(p.value)}
					title={p.unbound ? 'unbound' : p.value}
				>
					{#if !p.unbound && (!p.sentinel || isFeedSentinel(p.value))}
						<Handle
							id={`in:${p.name}`}
							type="target"
							position={Position.Left}
							isConnectable={false}
							class="io-dot"
						/>
					{:else}
						<span class="vdot" aria-hidden="true"></span>
					{/if}
					<span class="pname mono">{p.name}</span>
					{#if p.unbound}<span class="unset">unbound</span>
					{:else if p.sentinel}<span class="sent mono">{p.value}</span>
					{:else if p.value !== p.name}<span class="path mono">{p.value}</span>{/if}
				</div>
			{/each}

			{#each outputs as p (p.name)}
				<div class="io out" class:sentinel={p.sentinel} title={p.value}>
					{#if p.value && p.value !== p.name}<span class="path mono" class:sent={p.sentinel}
							>{p.value}</span
						>{/if}
					<span class="pname mono">{p.name}</span>
					{#if p.value}
						<Handle
							id={`out:${p.name}`}
							type="source"
							position={Position.Right}
							isConnectable={false}
							class="io-dot"
						/>
					{/if}
				</div>
			{/each}
		</div>
	{/if}

	{#if stage.type === 'agent' || stage.type === 'command' || stage.type === 'wait'}
		<Handle id="on_fail" type="source" position={Position.Bottom} class="h-fail" style="left:25%" />
		<Handle
			id="on_timeout"
			type="source"
			position={Position.Bottom}
			class="h-timeout"
			style="left:50%"
		/>
	{/if}
	{#if stage.type === 'agent' || stage.type === 'human_gate'}
		<Handle
			id="on_reject"
			type="source"
			position={Position.Bottom}
			class="h-reject"
			style="left:75%"
		/>
	{/if}
</div>

<style>
	/* Content glass, radius 14, a 3px top accent in the stage-type hue
	   (DESIGN 7.8). */
	.stage-node {
		position: relative;
		background: var(--cBg);
		backdrop-filter: var(--cBf);
		-webkit-backdrop-filter: var(--cBf);
		border: 1px solid var(--cLn);
		border-top: 3px solid var(--h);
		border-radius: 14px;
		box-shadow: var(--cSh);
		padding: 8px 12px 10px;
		min-width: 190px;
		max-width: 300px;
		font-size: 12px;
		color: var(--tx);
	}
	.stage-node.selected {
		box-shadow:
			0 0 0 1.5px var(--brand),
			0 0 22px var(--brandGlow),
			var(--cSh);
	}
	.head {
		display: flex;
		gap: 6px;
		align-items: center;
	}
	.id {
		font-weight: 600;
		font-size: 12px;
	}
	.entry {
		margin-left: auto;
		font-size: 9px;
		font-weight: 700;
		letter-spacing: 0.08em;
		color: var(--brand);
		border: 1px solid var(--brandLn);
		border-radius: 5px;
		padding: 0 5px;
		line-height: 15px;
	}
	.meta {
		font-size: 11px;
		color: var(--mut);
		margin-top: 4px;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.meta.unset {
		color: var(--amb);
		font-style: italic;
	}
	.ports {
		margin-top: 7px;
		padding-top: 5px;
		border-top: 1px solid var(--cLn);
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	/* One io row per port; the dot sits on the node's edge, and xyflow
	   measures handle geometry from the DOM. */
	.io {
		position: relative;
		display: flex;
		gap: 5px;
		align-items: baseline;
		font-size: 10px;
		line-height: 15px;
		white-space: nowrap;
		min-width: 0;
	}
	.io.out {
		justify-content: flex-end;
	}
	.pname {
		font-size: 10px;
		font-weight: 600;
	}
	.path,
	.unset,
	.sent {
		font-size: 10px;
		color: var(--fnt);
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}
	.sent {
		color: var(--vio);
	}
	.unset {
		font-style: italic;
		color: var(--amb);
	}
	/* An edge-carried input only fills on a retry: present, but not the
	   stage's main business. */
	.io.fed {
		opacity: 0.8;
	}
	/* Data dots read as plumbing, not as something you can grab: bound green,
	   unbound amber hollow, sentinel violet. Not connectable. */
	.stage-node :global(.io-dot),
	.vdot {
		position: absolute;
		width: 7px !important;
		height: 7px !important;
		min-width: 7px !important;
		min-height: 7px !important;
		border-radius: 50%;
		background: var(--grn) !important;
		border: 1.5px solid var(--body) !important;
		cursor: default;
		top: 50%;
	}
	.io.in :global(.io-dot),
	.io.in .vdot {
		left: -13px;
		transform: translate(-50%, -50%);
	}
	.io.out :global(.io-dot) {
		right: -13px;
		left: auto;
		transform: translate(50%, -50%);
	}
	.io.unbound .vdot {
		background: transparent !important;
		border: 1.5px solid var(--amb) !important;
	}
	.io.sentinel .vdot,
	.io.sentinel :global(.io-dot) {
		background: var(--vio) !important;
	}
	/* Control handles in their edge colour (DESIGN 3.6). */
	.stage-node :global(.h-in) {
		background: var(--mut);
	}
	.stage-node :global(.h-success) {
		background: var(--grn);
	}
	.stage-node :global(.h-fail) {
		background: var(--ros);
	}
	.stage-node :global(.h-timeout) {
		background: var(--tea);
	}
	.stage-node :global(.h-reject) {
		background: var(--amb);
	}
</style>
