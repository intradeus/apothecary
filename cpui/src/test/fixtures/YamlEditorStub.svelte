<script lang="ts">
	// YamlEditor without CodeMirror: its text, in a field a test can type in.
	let {
		value = $bindable(''),
		label,
		check,
		schema
	}: {
		value: string;
		label: string;
		check?: (yaml: string) => Promise<string[]>;
		schema?: Record<string, unknown>;
	} = $props();
</script>

<textarea
	aria-label={label}
	bind:value
	data-schema={schema ? 'loaded' : 'none'}
	data-check={check ? 'yes' : 'no'}></textarea>
