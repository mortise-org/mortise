<script lang="ts">
	import type { EnvVar } from '$lib/types';

	type Pair = { key: string; value: string; error?: string };

	interface Props {
		value: EnvVar[];
		placeholder?: string;
	}

	let { value = $bindable(), placeholder = '' }: Props = $props();

	type Mode = 'form' | 'raw';
	let mode = $state<Mode>('form');
	let fileInput = $state<HTMLInputElement | null>(null);

	// Internal editing state
	let pairs = $state<Pair[]>([]);
	let rawText = $state('');
	let rawErrors = $state<Map<number, string>>(new Map());

	// Track what we last emitted so we can distinguish "parent echoed our
	// value back" from "parent pushed new server data".  Plain `let` (not
	// $state) so it is invisible to the effect's dependency tracking.
	let lastEmitted: EnvVar[] = [];
	$effect(() => {
		const incoming = value ?? [];
		if (envVarsEqual(incoming, lastEmitted)) return;
		lastEmitted = incoming;
		const synced = envVarsToPairs(incoming);
		pairs = synced;
		rawText = pairsToRaw(synced);
	});

	function envVarsEqual(a: EnvVar[], b: EnvVar[]): boolean {
		if (a.length !== b.length) return false;
		for (let i = 0; i < a.length; i++) {
			if (a[i].name !== b[i].name) return false;
			if ((a[i].value ?? '') !== (b[i].value ?? '')) return false;
		}
		return true;
	}

	function envVarsToPairs(vars: EnvVar[]): Pair[] {
		return vars.map((v) => ({ key: v.name, value: v.value ?? '' }));
	}

	function pairsToEnvVars(list: Pair[]): EnvVar[] {
		return list
			.filter((p) => p.key.trim())
			.map((p) => ({ name: p.key, value: p.value }));
	}

	function pairsToRaw(list: Pair[]): string {
		return list
			.filter((p) => p.key.trim() || p.value.trim())
			.map((p) => `${p.key}=${p.value}`)
			.join('\n');
	}

	// Parses a .env-style blob. Returns pairs and a map of line-index -> error.
	function parseRaw(text: string): { pairs: Pair[]; errors: Map<number, string> } {
		const errors = new Map<number, string>();
		const out: Pair[] = [];
		const lines = text.split(/\r?\n/);
		lines.forEach((raw, idx) => {
			const line = raw.trim();
			if (!line) return;
			if (line.startsWith('#')) return;
			// Strip optional `export ` prefix that shells accept.
			const noExport = line.replace(/^export\s+/, '');
			const eq = noExport.indexOf('=');
			if (eq <= 0) {
				errors.set(idx, 'Expected KEY=value');
				return;
			}
			const key = noExport.slice(0, eq).trim();
			if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) {
				errors.set(idx, `Invalid key: "${key}"`);
				return;
			}
			let val = noExport.slice(eq + 1);
			// Strip surrounding quotes if matched.
			if (
				(val.startsWith('"') && val.endsWith('"') && val.length >= 2) ||
				(val.startsWith("'") && val.endsWith("'") && val.length >= 2)
			) {
				val = val.slice(1, -1);
			}
			out.push({ key, value: val });
		});
		return { pairs: out, errors };
	}

	function emit() {
		const result = pairsToEnvVars(pairs);
		lastEmitted = result;
		value = result;
	}

	function switchMode(target: Mode) {
		if (target === mode) return;
		if (mode === 'form' && target === 'raw') {
			rawText = pairsToRaw(pairs);
			rawErrors = new Map();
		} else if (mode === 'raw' && target === 'form') {
			const parsed = parseRaw(rawText);
			pairs = parsed.pairs;
			// Errors only relevant while inside raw mode.
			rawErrors = new Map();
			emit();
		}
		mode = target;
	}

	function addPair() {
		pairs = [...pairs, { key: '', value: '' }];
	}

	function removePair(i: number) {
		pairs = pairs.filter((_, idx) => idx !== i);
		emit();
	}

	function onPairChange() {
		emit();
	}

	function onRawInput() {
		const parsed = parseRaw(rawText);
		rawErrors = parsed.errors;
		pairs = parsed.pairs;
		emit();
	}

	function onFormPaste(e: ClipboardEvent, index: number, field: 'key' | 'value') {
		const text = e.clipboardData?.getData('text') ?? '';
		if (!text.includes('\n') && !/^\s*[A-Za-z_][A-Za-z0-9_]*\s*=/.test(text)) {
			return; // let the browser handle a normal paste
		}
		// If the pasted content looks multi-line or KEY=value, expand into rows.
		if (text.includes('\n') || text.includes('=')) {
			e.preventDefault();
			const parsed = parseRaw(text);
			if (parsed.pairs.length === 0) return;
			const before = pairs.slice(0, index);
			const current = pairs[index];
			const after = pairs.slice(index + 1);
			// If the current row is empty, replace it. Otherwise keep it.
			const keepCurrent = current && (current.key.trim() || current.value.trim());
			const merged = keepCurrent ? [...before, current, ...parsed.pairs, ...after] : [...before, ...parsed.pairs, ...after];
			pairs = merged;
			emit();
			// hint to reference `field` param to avoid unused warnings
			void field;
		}
	}

	function onRawPaste() {
		// Native textarea paste is fine; rely on onRawInput to re-parse.
	}

	function openFilePicker() {
		fileInput?.click();
	}

	async function onFileChosen(e: Event) {
		const input = e.target as HTMLInputElement;
		const file = input.files?.[0];
		if (!file) return;
		const text = await file.text();
		const parsed = parseRaw(text);
		pairs = parsed.pairs;
		rawText = text;
		rawErrors = parsed.errors;
		emit();
		// Reset so picking the same file again still fires change.
		input.value = '';
	}
</script>

<div class="rounded-md border border-surface-600 bg-surface-800">
	<div class="flex items-center justify-between border-b border-surface-600 px-2 py-1.5">
		<div class="flex gap-1">
			<button
				type="button"
				onclick={() => switchMode('form')}
				class="rounded px-2.5 py-1 text-xs transition-colors {mode === 'form'
					? 'bg-surface-600 text-white'
					: 'text-gray-400 hover:text-white'}"
			>
				Form
			</button>
			<button
				type="button"
				onclick={() => switchMode('raw')}
				class="rounded px-2.5 py-1 text-xs transition-colors {mode === 'raw'
					? 'bg-surface-600 text-white'
					: 'text-gray-400 hover:text-white'}"
			>
				Raw
			</button>
		</div>
		<div class="flex items-center gap-2">
			<button
				type="button"
				onclick={openFilePicker}
				class="rounded px-2 py-1 text-xs text-gray-400 transition-colors hover:text-white"
			>
				Import .env
			</button>
			<input
				bind:this={fileInput}
				type="file"
				accept=".env,.txt,text/plain"
				onchange={onFileChosen}
				class="hidden"
			/>
		</div>
	</div>

	{#if mode === 'form'}
		<div class="p-3">
			{#if pairs.length === 0}
				<p class="mb-2 text-xs text-gray-500">
					{placeholder || 'No variables yet. Add one below, or paste a .env block.'}
				</p>
			{/if}
			{#each pairs as pair, i (i)}
				<div class="mb-2 flex gap-2">
					<input
						type="text"
						bind:value={pair.key}
						oninput={onPairChange}
						onpaste={(e) => onFormPaste(e, i, 'key')}
						placeholder="KEY"
						spellcheck="false"
						class="w-1/3 rounded-md border border-surface-600 bg-surface-700 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
					/>
					<input
						type="text"
						bind:value={pair.value}
						oninput={onPairChange}
						onpaste={(e) => onFormPaste(e, i, 'value')}
						placeholder="value"
						spellcheck="false"
						class="flex-1 rounded-md border border-surface-600 bg-surface-700 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
					/>
					<button
						type="button"
						onclick={() => removePair(i)}
						aria-label="Remove variable"
						class="px-2 text-gray-500 hover:text-danger"
					>
						&times;
					</button>
				</div>
			{/each}
			<button
				type="button"
				onclick={addPair}
				class="mt-1 text-xs text-accent hover:text-accent-hover"
			>
				+ Add variable
			</button>
		</div>
	{:else}
		<div class="p-3">
			<div class="relative">
				<textarea
					bind:value={rawText}
					oninput={onRawInput}
					onpaste={onRawPaste}
					placeholder={placeholder || 'KEY=value\nANOTHER=thing\n# comments are allowed'}
					spellcheck="false"
					rows="10"
					class="block w-full resize-y rounded-md border border-surface-600 bg-surface-700 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
				></textarea>
			</div>
			{#if rawErrors.size > 0}
				<ul class="mt-2 space-y-1">
					{#each [...rawErrors.entries()] as [lineIdx, msg]}
						<li class="text-xs text-danger" title={msg}>
							Line {lineIdx + 1}: {msg}
						</li>
					{/each}
				</ul>
			{/if}
		</div>
	{/if}
</div>
