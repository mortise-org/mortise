import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { svelteTesting } from '@testing-library/svelte/vite';
import { defineConfig } from 'vitest/config';

export default defineConfig({
	plugins: [tailwindcss(), sveltekit(), svelteTesting()],
	server: {
		proxy: {
			'/api': 'http://127.0.0.1:8080'
		}
	},
	test: {
		environment: 'jsdom',
		setupFiles: ['./tests/setup.ts'],
		exclude: ['tests/e2e/**', '**/node_modules/**', '**/.git/**']
	}
});
