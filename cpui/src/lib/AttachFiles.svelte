<script lang="ts" module>
	import { api as apiMod } from '$lib/api';
	import type { UploadLimits as UL } from '$lib/domain';
	let limitsPromise: Promise<UL | null> | null = null;
	function limitsOnce(): Promise<UL | null> {
		if (!limitsPromise) {
			limitsPromise = apiMod.files.limits().catch(() => null);
		}
		return limitsPromise;
	}
</script>

<script lang="ts">
	// Attachments picker (DESIGN 4.15): a drop zone plus removable chips with
	// thumbnails. The parent owns the selected files (bindable) and uploads
	// them on submit via api.files.upload. Documents/images attached here reach
	// the agent.
	//
	// The server's upload policy is mirrored client-side (fetched once): a pick
	// that would be rejected — too big, too many, off the MIME allowlist — is
	// refused INLINE with the reason, instead of after a failed round trip.
	import { fmtBytes } from '$lib/format';
	import type { UploadLimits } from '$lib/domain';

	let {
		files = $bindable([]),
		label = 'Drop files or browse',
		compact = false
	}: { files?: File[]; label?: string; compact?: boolean } = $props();

	let inputEl = $state<HTMLInputElement | null>(null);
	let refused = $state<{ name: string; size: number; why: string }[]>([]);
	let over = $state(false);

	// One fetch per session, shared by every picker instance.
	let limits = $state<UploadLimits | null>(null);
	$effect(() => {
		limitsOnce().then((l) => (limits = l));
	});

	const kinds = $derived.by(() => {
		if (!limits) return '';
		const k = new Set<string>();
		for (const m of limits.mimeAllow) {
			if (m.startsWith('image/')) k.add('images');
			else if (m.includes('pdf')) k.add('pdf');
			else if (m.includes('markdown')) k.add('md');
			else if (m.startsWith('text/')) k.add('text');
		}
		return [...k].join(', ');
	});

	const urls = new Map<File, string>();
	function preview(f: File): string {
		let u = urls.get(f);
		if (!u) {
			u = URL.createObjectURL(f);
			urls.set(f, u);
		}
		return u;
	}

	function allowed(l: UploadLimits, mime: string): boolean {
		const m = mime.toLowerCase().split(';')[0].trim();
		if (!m) return true; // unknown here — let the server sniff it
		return l.mimeAllow.some((a) =>
			a.endsWith('/*') ? m.startsWith(a.slice(0, -1)) : m === a.toLowerCase()
		);
	}

	function take(list: FileList | File[] | null | undefined) {
		refused = [];
		if (!list) return;
		const add: File[] = [];
		const no: typeof refused = [];
		for (const f of Array.from(list)) {
			if (limits && f.size > limits.maxBytes) {
				no.push({ name: f.name, size: f.size, why: `over the ${fmtBytes(limits.maxBytes)} limit` });
			} else if (limits && !allowed(limits, f.type)) {
				no.push({ name: f.name, size: f.size, why: `${f.type || 'unknown type'} is not allowed` });
			} else if (limits && files.length + add.length >= limits.maxFiles) {
				no.push({ name: f.name, size: f.size, why: `at most ${limits.maxFiles} files` });
			} else {
				add.push(f);
			}
		}
		files = [...files, ...add];
		refused = no;
	}

	function onPick(e: Event) {
		take((e.target as HTMLInputElement).files);
		if (inputEl) inputEl.value = ''; // allow re-picking the same file
	}
	function onDrop(e: DragEvent) {
		e.preventDefault();
		over = false;
		take(e.dataTransfer?.files);
	}
	const remove = (i: number) => (files = files.filter((_, j) => j !== i));
</script>

<div class="attach">
	<button
		type="button"
		class="drop"
		class:over
		class:compact
		onclick={() => inputEl?.click()}
		ondragover={(e) => {
			e.preventDefault();
			over = true;
		}}
		ondragleave={() => (over = false)}
		ondrop={onDrop}
	>
		<span class="clip">⎘</span>
		<span>{label}</span>
		{#if limits && !compact}
			<span class="lim">· max {fmtBytes(limits.maxBytes)}{kinds ? ` · ${kinds}` : ''}</span>
		{/if}
	</button>
	<input bind:this={inputEl} type="file" multiple hidden onchange={onPick} />
	{#if files.length || refused.length}
		<div class="chips">
			{#each files as f, i (i)}
				<span class="fc">
					{#if f.type.startsWith('image/')}<img src={preview(f)} alt="" />{:else}<span class="ico"
							>▤</span
						>{/if}
					<span class="name" title={f.name}>{f.name}</span>
					<span class="size mono">{fmtBytes(f.size)}</span>
					<button type="button" class="x" onclick={() => remove(i)} aria-label="Remove {f.name}"
						>×</button
					>
				</span>
			{/each}
			{#each refused as r (r.name)}
				<span class="fc bad" title={r.why}>
					<span class="ico">!</span>
					<span class="name">{r.name}</span>
					<span class="size mono">{r.why}</span>
				</span>
			{/each}
		</div>
	{/if}
</div>

<style>
	.attach {
		display: flex;
		flex-direction: column;
		gap: 8px;
		min-width: 0;
	}
	.drop {
		all: unset;
		box-sizing: border-box;
		cursor: pointer;
		display: flex;
		align-items: center;
		justify-content: center;
		gap: 6px;
		padding: 12px;
		border: 1px dashed var(--ln2);
		border-radius: 12px;
		color: var(--mut);
		font-size: 12px;
		text-align: center;
		flex-wrap: wrap;
	}
	.drop.compact {
		justify-content: flex-start;
		padding: 6px 10px;
	}
	.drop:hover,
	.drop.over {
		border-color: var(--brand);
		background: var(--brandBg);
		color: var(--tx);
	}
	.drop:focus-visible {
		outline: 2px solid var(--brand);
		outline-offset: 2px;
	}
	.clip {
		font-size: 13px;
	}
	.lim {
		color: var(--fnt);
	}
	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}
	.fc {
		display: inline-flex;
		align-items: center;
		gap: 8px;
		padding: 3px 4px 3px 3px;
		border-radius: 10px;
		background: var(--p2);
		border: 1px solid var(--ln);
		font-size: 12px;
		max-width: 280px;
	}
	.fc.bad {
		background: var(--rosBg);
		border-color: var(--rosLn);
		color: var(--ros);
		padding-right: 10px;
	}
	.fc img,
	.ico {
		flex: none;
		width: 28px;
		height: 28px;
		border-radius: 7px;
		object-fit: cover;
	}
	.ico {
		display: flex;
		align-items: center;
		justify-content: center;
		background: var(--code);
		color: var(--mut);
		font-size: 13px;
	}
	.bad .ico {
		color: var(--ros);
		font-weight: 700;
	}
	.name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		min-width: 0;
	}
	.size {
		color: var(--mut);
		font-size: 10.5px;
		white-space: nowrap;
	}
	.bad .size {
		color: var(--ros);
	}
	.x {
		all: unset;
		cursor: pointer;
		width: 18px;
		height: 18px;
		border-radius: 6px;
		display: flex;
		align-items: center;
		justify-content: center;
		color: var(--mut);
	}
	.x:hover {
		background: var(--p3);
		color: var(--tx);
	}
</style>
