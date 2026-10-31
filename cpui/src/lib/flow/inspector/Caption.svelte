<script lang="ts">
	// A panel's small label over what follows it, a little clear of the
	// line before.
	let { text }: { text: string } = $props();
</script>

<div class="caption">{text}</div>

<style>
	.caption {
		margin-top: 4px;
	}
</style>
