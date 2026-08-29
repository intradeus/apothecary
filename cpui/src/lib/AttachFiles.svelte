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
	// A file picker with removable chips + image thumbnails. The parent owns
	// the selected files (bindable) and uploads them on submit via
	// api.uploadAttachments. Documents/images attached here reach the agent.
	//
	// The server's upload policy is mirrored client-side (fetched once): a pick
	// that would be rejected — too big, too many, off the MIME allowlist — is
	// refused INLINE with the reason, instead of after a failed round trip.
	import { api } from '$lib/api';
	import { fmtBytes } from '$lib/format';
	import type { UploadLimits } from '$lib/domain';

	let { files = $bindable([]), label = 'Attach files' }: { files?: File[]; label?: string } =
		$props();

	let inputEl = $state<HTMLInputElement | null>(null);
	let rejected = $state('');

	// One fetch per session, shared by every picker instance.
	let limits = $state<UploadLimits | null>(null);
	$effect(() => {
		limitsOnce().then((l) => (limits = l));
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

	function onPick(e: Event) {
		const picked = (e.target as HTMLInputElement).files;
		rejected = '';
		if (picked) {
			const add: File[] = [];
			const refuse: string[] = [];
			for (const f of Array.from(picked)) {
				if (limits && f.size > limits.maxBytes) {
					refuse.push(`${f.name}: over the ${fmtBytes(limits.maxBytes)} limit`);
				} else if (limits && !allowed(limits, f.type)) {
					refuse.push(`${f.name}: type ${f.type || 'unknown'} is not allowed`);
				} else if (limits && files.length + add.length >= limits.maxFiles) {
					refuse.push(`${f.name}: at most ${limits.maxFiles} files`);
				} else {
					add.push(f);
				}
			}
			files = [...files, ...add];
			if (refuse.length) rejected = refuse.join(' · ');
		}
		if (inputEl) inputEl.value = ''; // allow re-picking the same file
	}
	const remove = (i: number) => (files = files.filter((_, j) => j !== i));
</script>

<div class="attach">
	<button type="button" class="small" onclick={() => inputEl?.click()}>📎 {label}</button>
	<input bind:this={inputEl} type="file" multiple hidden onchange={onPick} />
	{#if rejected}<div class="error small">{rejected}</div>{/if}
	{#if files.length}
		<div class="chips">
			{#each files as f, i (i)}
				<span class="chip">
					{#if f.type.startsWith('image/')}<img src={preview(f)} alt={f.name} />{/if}
					<span class="name" title={f.name}>{f.name}</span>
					<button type="button" class="x" onclick={() => remove(i)} aria-label="Remove">×</button>
				</span>
			{/each}
		</div>
	{/if}
</div>

<style>
	.attach {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}
	.chip {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 3px 6px;
		border: 1px solid var(--border, #ccc);
		border-radius: 6px;
		font-size: 12px;
		max-width: 220px;
	}
	.chip img {
		width: 22px;
		height: 22px;
		object-fit: cover;
		border-radius: 3px;
	}
	.chip .name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.chip .x {
		border: none;
		background: none;
		cursor: pointer;
		font-size: 14px;
		line-height: 1;
		padding: 0 2px;
	}
</style>
