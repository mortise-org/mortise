<script lang="ts">
	import type { App } from '$lib/types';
	import BindingsPicker from '$lib/components/BindingsPicker.svelte';
	import { Plus, Trash2, Link, Upload, FileText, X, Eye, EyeOff } from 'lucide-svelte';

	type EnvEntry = {
		name: string;
		value: string;
		source?: string;
		revealed?: boolean;
		bindingRef?: string;
		bindingKey?: string;
	};

	type SectionState = {
		entries: EnvEntry[];
		loading: boolean;
		saving: boolean;
		error: string;
		editedKeys: Set<string>;
		originalEntries: EnvEntry[];
		showNewRow: boolean;
		newKey: string;
		newValue: string;
		showPicker: boolean;
		rawMode: boolean;
		rawText: string;
	};

	let {
		section,
		title,
		subtitle = '',
		project = '',
		app = undefined,
		activeEnv = '',
		showBindingPicker = false,
		showSourceBadge = true,
		onSave,
		onAdd,
		onDelete,
		onImportRaw,
		onValueEdit,
		onKeyPaste,
		onToggleReveal,
		onBindingSelect,
		onSecretSelect,
		onSetRawMode,
		onSetRawText,
		onSetShowNewRow,
		onSetNewKey,
		onSetNewValue,
		onSetShowPicker
	}: {
		section: SectionState;
		title: string;
		subtitle?: string;
		project?: string;
		app?: App;
		activeEnv?: string;
		showBindingPicker?: boolean;
		showSourceBadge?: boolean;
		onSave: () => void;
		onAdd: () => void;
		onDelete: (idx: number) => void;
		onImportRaw: () => void;
		onValueEdit: (idx: number, value: string) => void;
		onKeyPaste: (e: ClipboardEvent) => void;
		onToggleReveal: (idx: number) => void;
		onBindingSelect?: (ref: string, key: string) => void;
		onSecretSelect?: (secretName: string) => void;
		onSetRawMode: (v: boolean) => void;
		onSetRawText: (v: string) => void;
		onSetShowNewRow: (v: boolean) => void;
		onSetNewKey: (v: string) => void;
		onSetNewValue: (v: string) => void;
		onSetShowPicker?: (v: boolean) => void;
	} = $props();

	function sourceBadge(source?: string): { label: string; classes: string } | null {
		switch (source) {
			case 'binding': return { label: 'binding', classes: 'bg-info/10 text-info' };
			case 'generated': return { label: 'generated', classes: 'bg-warning/10 text-warning' };
			case 'shared': return { label: 'project', classes: 'bg-accent/10 text-accent' };
			default: return null;
		}
	}

	function isFromBinding(entry: EnvEntry): boolean {
		return !!entry.bindingRef && !!entry.bindingKey;
	}

	function isAutoInjectedBinding(entry: EnvEntry): boolean {
		return entry.source === 'binding' && !entry.bindingRef;
	}
</script>

<div class="rounded-lg border border-surface-600 bg-surface-900">
	<div class="flex items-center justify-between px-3 py-2.5">
		<div class="flex items-center gap-2">
			<span class="text-sm font-medium text-white">{title}</span>
			{#if subtitle}
				<span class="text-xs text-gray-500">{subtitle}</span>
			{/if}
		</div>
		<div class="flex items-center gap-2">
			<div class="flex gap-1">
				<button type="button" onclick={() => onSetRawMode(false)}
					class="{!section.rawMode ? 'text-white bg-surface-700' : 'text-gray-500 hover:text-white'} text-xs px-2 py-1 rounded">
					<FileText class="inline h-3 w-3 mr-1" />Table
				</button>
				<button type="button" onclick={() => { onSetRawMode(true); onSetRawText(section.entries.map(e => `${e.name}=${e.value}`).join('\n')); }}
					class="{section.rawMode ? 'text-white bg-surface-700' : 'text-gray-500 hover:text-white'} text-xs px-2 py-1 rounded">
					<Upload class="inline h-3 w-3 mr-1" />Raw
				</button>
			</div>
			{#if section.editedKeys.size > 0 && !section.rawMode}
				<button type="button" onclick={onSave} disabled={section.saving}
					class="rounded-md bg-accent px-3 py-1 text-xs font-medium text-white hover:bg-accent-hover disabled:opacity-50">
					{section.saving ? 'Saving...' : `Save ${section.editedKeys.size} change${section.editedKeys.size === 1 ? '' : 's'}`}
				</button>
			{/if}
			{#if !section.rawMode}
				<button type="button" onclick={() => onSetShowNewRow(true)}
					class="flex items-center gap-1 rounded-md border border-surface-600 px-2 py-1 text-xs text-gray-400 hover:bg-surface-700 hover:text-white">
					<Plus class="h-3.5 w-3.5" />
				</button>
			{/if}
		</div>
	</div>

	<div class="border-t border-surface-600">
		{#if section.error}
			<div class="px-3 py-2 text-xs text-danger">{section.error}</div>
		{/if}

		{#if section.loading}
			<div class="flex items-center justify-center py-6">
				<div class="inline-block h-4 w-4 animate-spin rounded-full border-2 border-gray-500 border-t-transparent"></div>
			</div>
		{:else if section.rawMode}
			<div class="p-3 space-y-3">
				<p class="text-xs text-gray-500">Edit as .env format. Save replaces all variables.</p>
				<textarea value={section.rawText} oninput={(e) => onSetRawText((e.target as HTMLTextAreaElement).value)} rows={10}
					placeholder="KEY=value&#10;ANOTHER_KEY=another_value"
					class="w-full resize-y rounded-md border border-surface-600 bg-surface-700 px-3 py-2 font-mono text-xs text-white placeholder-gray-500 outline-none focus:border-accent"></textarea>
				<div class="flex gap-2">
					<button type="button" onclick={onImportRaw} disabled={!section.rawText.trim() || section.saving}
						class="rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
						{section.saving ? 'Saving...' : 'Save'}
					</button>
					<button type="button" onclick={() => onSetRawMode(false)}
						class="rounded-md border border-surface-600 px-3 py-1.5 text-sm text-gray-400 hover:bg-surface-700 hover:text-white">
						Cancel
					</button>
				</div>
			</div>
		{:else}
			{#if section.showNewRow}
				<div class="flex items-center gap-2 border-b border-surface-600 px-3 py-2 bg-surface-700/30">
					<input type="text" value={section.newKey} oninput={(e) => onSetNewKey((e.target as HTMLInputElement).value)}
						placeholder="VARIABLE_NAME"
						onpaste={(e) => onKeyPaste(e)}
						onkeydown={(e) => { if (e.key === 'Enter' && section.newKey.trim()) onAdd(); }}
						class="w-[40%] rounded-md border border-surface-600 bg-surface-800 px-2.5 py-1.5 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
					<div class="relative flex-1">
						<input type="text" value={section.newValue} oninput={(e) => onSetNewValue((e.target as HTMLInputElement).value)}
							placeholder="value or binding ref"
							onkeydown={(e) => { if (e.key === 'Enter' && section.newKey.trim()) onAdd(); }}
							class="w-full rounded-md border border-surface-600 bg-surface-800 px-2.5 py-1.5 text-sm text-white placeholder-gray-500 outline-none focus:border-accent {showBindingPicker ? 'pr-8' : ''}" />
						{#if showBindingPicker && onBindingSelect && onSetShowPicker}
							<button type="button" onclick={() => onSetShowPicker?.(!section.showPicker)}
								class="absolute right-2 top-1/2 -translate-y-1/2 text-gray-500 hover:text-accent" title="Insert from binding or secret">
								<Link class="h-3.5 w-3.5" />
							</button>
							{#if section.showPicker && project && app}
								<BindingsPicker {project} {app} {activeEnv}
									onBindingSelect={(ref, key) => onBindingSelect?.(ref, key)}
									onSecretSelect={(name) => onSecretSelect?.(name)}
									onClose={() => onSetShowPicker?.(false)} />
							{/if}
						{/if}
					</div>
					<button type="button" onclick={onAdd} disabled={!section.newKey.trim() || section.saving}
						class="rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover disabled:opacity-50">Add</button>
					<button type="button" onclick={() => { onSetShowNewRow(false); onSetNewKey(''); onSetNewValue(''); }}
						class="rounded p-1.5 text-gray-500 hover:text-white"><X class="h-3.5 w-3.5" /></button>
				</div>
			{/if}

			{#if section.entries.length === 0 && !section.showNewRow}
				<div class="py-8 text-center text-xs text-gray-500">
					No variables set. Click + to add one, or paste a .env file.
				</div>
			{:else}
				{#each section.entries as entry, idx}
					<div class="group flex items-center gap-2 border-b border-surface-600 px-3 py-2 hover:bg-surface-700/30">
						<div class="flex items-center gap-2 w-[40%] min-w-0">
							<span class="font-mono text-sm text-gray-200 truncate">{entry.name}</span>
							{#if showSourceBadge && sourceBadge(entry.source)}
								<span class="shrink-0 inline-flex items-center rounded-full px-1.5 py-0.5 text-[10px] font-medium {sourceBadge(entry.source)?.classes}">{sourceBadge(entry.source)?.label}</span>
							{/if}
							{#if section.editedKeys.has(entry.name)}
								<span class="shrink-0 inline-flex items-center rounded-full bg-accent/10 px-1.5 py-0.5 text-[10px] font-medium text-accent">edited</span>
							{/if}
						</div>
						<div class="flex-1 flex items-center gap-1 min-w-0">
							{#if isFromBinding(entry)}
								<!-- fromBinding: show ref → key as display, reveal shows resolved value -->
								{#if entry.revealed}
									<span class="w-full px-1 py-0.5 font-mono text-xs text-gray-400 truncate">{entry.value}</span>
								{:else}
									<span class="w-full px-1 py-0.5 font-mono text-xs text-info/70 truncate">{entry.bindingRef} → {entry.bindingKey}</span>
								{/if}
								<button type="button" onclick={() => onToggleReveal(idx)}
									class="shrink-0 p-1 text-gray-600 hover:text-gray-400" title={entry.revealed ? 'Show reference' : 'Show resolved value'}>
									{#if entry.revealed}
										<EyeOff class="h-3.5 w-3.5" />
									{:else}
										<Eye class="h-3.5 w-3.5" />
									{/if}
								</button>
							{:else if entry.revealed}
								<input type="text" value={entry.value}
									oninput={(e) => onValueEdit(idx, (e.target as HTMLInputElement).value)}
									readonly={isAutoInjectedBinding(entry)}
									class="w-full rounded border border-transparent bg-transparent px-1 py-0.5 font-mono text-xs text-gray-400 outline-none focus:border-surface-500 focus:bg-surface-700 hover:border-surface-600 {isAutoInjectedBinding(entry) ? 'cursor-default' : ''}" />
							{:else}
								<button type="button" onclick={() => onToggleReveal(idx)}
									class="w-full text-left px-1 py-0.5 font-mono text-xs text-gray-500 hover:text-gray-400 truncate">
									{'*'.repeat(Math.min(entry.value.length || 7, 20))}
								</button>
							{/if}
							{#if !isFromBinding(entry)}
								<button type="button" onclick={() => onToggleReveal(idx)}
									class="shrink-0 p-1 text-gray-600 hover:text-gray-400" title={entry.revealed ? 'Hide' : 'Reveal'}>
									{#if entry.revealed}
										<EyeOff class="h-3.5 w-3.5" />
									{:else}
										<Eye class="h-3.5 w-3.5" />
									{/if}
								</button>
							{/if}
						</div>
						{#if !isAutoInjectedBinding(entry)}
							<button type="button" onclick={() => onDelete(idx)}
								class="shrink-0 rounded p-1 text-gray-500 hover:text-danger transition-colors">
								<Trash2 class="h-3.5 w-3.5" />
							</button>
						{:else}
							<div class="w-[26px]"></div>
						{/if}
					</div>
				{/each}
			{/if}
		{/if}
	</div>
</div>
