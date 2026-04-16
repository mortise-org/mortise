// Current-project context. A tiny Svelte 5 runed store persisted in
// localStorage, so the project switcher and any page that wants to know
// "what's the current project?" agree without prop-drilling.
//
// This intentionally stays dumb: the URL (`/projects/{p}/...`) is still the
// source of truth while on a project page. The stored value is only used
// by the switcher to remember the last-active project and to drive the
// default landing if we ever want to.

import { browser } from '$app/environment';

const STORAGE_KEY = 'mortise.currentProject';

class CurrentProject {
	#value = $state<string | null>(null);

	constructor() {
		if (browser) {
			this.#value = localStorage.getItem(STORAGE_KEY);
		}
	}

	get value(): string | null {
		return this.#value;
	}

	set(name: string | null): void {
		this.#value = name;
		if (!browser) return;
		if (name) {
			localStorage.setItem(STORAGE_KEY, name);
		} else {
			localStorage.removeItem(STORAGE_KEY);
		}
	}
}

export const currentProject = new CurrentProject();
